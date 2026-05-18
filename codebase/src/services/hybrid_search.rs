//! Hybrid search service combining Dense (Vector) and Sparse (BM25) retrieval channels.
//!
//! Uses Reciprocal Rank Fusion (RRF) to merge results from both channels,
//! providing better recall and precision than either channel alone.

use crate::services::embedding_service::{EmbeddingService, SearchResult};
use crate::storage::traits_bm25::{CandidateSource, FusedCandidate, TextSearchProvider, TextSearchResult};
use anyhow::{Result, anyhow};
use std::collections::HashMap;
use std::sync::Arc;
use tracing::{debug, warn};

/// Configuration for hybrid search behavior.
#[derive(Debug, Clone)]
pub struct HybridSearchConfig {
    /// RRF fusion constant. Higher = ranks matter less.
    /// Industry standard is 60. See: https://plg.uwaterloo.ca/~gvcormac/cormacksigir09-rrf.pdf
    pub rrf_k: f64,
    /// Number of dense (vector) results to fetch per fusion query.
    /// Should be >= top_k to ensure enough candidates for fusion.
    pub dense_limit: usize,
    /// Number of sparse (BM25) results to fetch per fusion query.
    /// Should be >= top_k to ensure enough candidates for fusion.
    pub sparse_limit: usize,
    /// Whether to enable the sparse (BM25) channel.
    /// When false, falls back to dense-only search.
    pub enable_sparse: bool,
    /// Timeout for the entire hybrid search operation (milliseconds).
    /// 0 means no timeout.
    pub timeout_ms: u64,
}

impl Default for HybridSearchConfig {
    fn default() -> Self {
        Self {
            rrf_k: 60.0,
            dense_limit: 100,
            sparse_limit: 100,
            enable_sparse: true,
            timeout_ms: 0,
        }
    }
}

/// The hybrid search service that orchestrates dual-channel retrieval.
///
/// Architecture:
/// ```text
///    Query
///      │
///      ├──→ EmbeddingService.search() ──→ DenseResults
///      │
///      └──→ TextSearchProvider.search() ──→ SparseResults (BM25)
///      │
///      └──→ RRF Fusion ──→ FusedCandidates (sorted by fused score)
/// ```
///
/// Internal metadata structure used to track line info during RRF fusion.
#[derive(Clone)]
struct DenseMeta {
    file_path: String,
    symbol_name: String,
    code_block: String,
    line_start: usize,
    line_end: usize,
}

impl From<&SearchResult> for DenseMeta {
    fn from(sr: &SearchResult) -> Self {
        let line_count = sr.code_block.lines().count();
        Self {
            file_path: sr.file_path.clone(),
            symbol_name: sr.symbol_name.clone(),
            code_block: sr.code_block.clone(),
            line_start: 0,
            line_end: line_count,
        }
    }
}

pub struct HybridSearchService {
    dense: Arc<EmbeddingService>,
    sparse: Arc<dyn TextSearchProvider>,
    config: HybridSearchConfig,
}

impl HybridSearchService {
    /// Create a new hybrid search service.
    pub fn new(
        dense: Arc<EmbeddingService>,
        sparse: Arc<dyn TextSearchProvider>,
        config: HybridSearchConfig,
    ) -> Self {
        Self { dense, sparse, config }
    }

    /// Execute hybrid search: query both channels, fuse results via RRF.
    ///
    /// # Arguments
    /// * `query` - The search query string
    /// * `limit` - Number of final results to return
    ///
    /// # Returns
    /// A vector of `FusedCandidate` sorted by fused RRF score (descending).
    /// If sparse channel is disabled or unavailable, falls back to dense-only.
    pub async fn search(&self, query: &str, limit: usize) -> Result<Vec<FusedCandidate>> {
        if query.is_empty() {
            return Ok(Vec::new());
        }

        let dense_limit = self.config.dense_limit.max(limit);
        let sparse_limit = if self.config.enable_sparse {
            self.config.sparse_limit.max(limit)
        } else {
            0
        };

        // Dispatch both channels in parallel
        let dense_future = self.dense.search(query, dense_limit);
        let sparse_future = if self.config.enable_sparse {
            Some(Box::pin(self.sparse.search(query, sparse_limit)))
        } else {
            None
        };

        let dense_res = dense_future.await;

        // Handle sparse results with graceful degradation
        let sparse_res_opt = match sparse_future {
            Some(fut) => Some(fut.await),
            None => {
                debug!("Sparse channel disabled in config");
                None
            }
        };

        // Dense results are required — if they fail, the whole search fails
        let dense_raw = dense_res.map_err(|e| {
            anyhow!("Dense (vector) search failed: {}", e)
        })?;

        // Handle sparse results with graceful degradation
        let sparse_results: Option<Vec<TextSearchResult>> = match sparse_res_opt {
            None => {
                debug!("Sparse channel disabled in config");
                None
            }
            Some(Ok(results)) => {
                debug!("Sparse channel returned {} results", results.len());
                Some(results)
            }
            Some(Err(e)) => {
                warn!(
                    "Sparse (BM25) channel failed, falling back to dense-only: {}",
                    e
                );
                // Return dense results as dense-only candidates
                return Ok(
                    dense_raw
                        .into_iter()
                        .map(|sr| FusedCandidate {
                            snippet_id: format!("{}#{}", sr.file_path, sr.symbol_name),
                            final_score: sr.score as f64,
                            file_path: sr.file_path,
                            symbol_name: sr.symbol_name,
                            symbol_type: String::new(),
                            language: String::new(),
                            line_start: 0,
                            line_end: 0,
                            code_block: sr.code_block,
                            source: CandidateSource::DenseOnly,
                        })
                        .take(limit)
                        .collect(),
                );
            }
        };

        // Perform RRF fusion
        let fused = self.reciprocal_rank_fusion(dense_raw, sparse_results.unwrap_or_default(), limit);
        debug!("Hybrid search returned {} fused candidates", fused.len());
        Ok(fused)
    }

    /// Reciprocal Rank Fusion (RRF) for combining ranked lists.
    ///
    /// Formula: score(d) = Σ 1 / (k + rank(d))
    /// where k is the RRF constant (default 60), rank(d) is the 0-based position of document d
    /// in a ranked list, and the sum is over all channels that returned d.
    ///
    /// Documents appearing in both channels get a higher fused score.
    fn reciprocal_rank_fusion(
        &self,
        dense: Vec<SearchResult>,
        sparse: Vec<TextSearchResult>,
        limit: usize,
    ) -> Vec<FusedCandidate> {
        let k = self.config.rrf_k;

        // Accumulate scores per snippet_id, along with metadata
        let mut score_map: HashMap<String, f64> = HashMap::new();
        let mut meta_map: HashMap<String, DenseMeta> = HashMap::new();

        // Process dense channel
        for (rank, dr) in dense.iter().enumerate() {
            let snippet_id = format!("{}#{}", dr.file_path, dr.symbol_name);
            let rrf_score = 1.0 / (k + rank as f64 + 1.0);
            *score_map.entry(snippet_id.clone()).or_insert(0.0) += rrf_score;
            meta_map.entry(snippet_id).or_insert_with(|| DenseMeta::from(dr));
        }

        // Process sparse channel — fill in line_start from sparse results
        for (rank, sr) in sparse.iter().enumerate() {
            let rrf_score = 1.0 / (k + rank as f64 + 1.0);
            *score_map.entry(sr.snippet_id.clone()).or_insert(0.0) += rrf_score;

            // If sparse has line info but dense doesn't, merge it
            if let Some(meta) = meta_map.get_mut(&sr.snippet_id) {
                if meta.line_start == 0 && sr.line_start > 0 {
                    meta.line_start = sr.line_start;
                }
            }
        }

        // Sort by fused score descending
        let mut ranked: Vec<(String, f64)> = score_map.into_iter().collect();
        ranked.sort_by(|a, b| b.1.partial_cmp(&a.1).unwrap_or(std::cmp::Ordering::Equal));

        // Take top-K and build FusedCandidate structs
        ranked
            .into_iter()
            .take(limit)
            .filter_map(|(snippet_id, fused_score)| {
                // Determine source
                let dense_count = dense.iter().any(|dr| {
                    format!("{}#{}", dr.file_path, dr.symbol_name) == snippet_id
                }) as u32;
                let sparse_count = sparse.iter().any(|sr| sr.snippet_id == snippet_id) as u32;
                let source = match (dense_count > 0, sparse_count > 0) {
                    (true, true) => CandidateSource::Fused,
                    (true, false) => CandidateSource::DenseOnly,
                    (false, true) => CandidateSource::SparseOnly,
                    (false, false) => return None,
                };

                // Get metadata from dense result
                let meta = meta_map.get(&snippet_id)?;

                Some(FusedCandidate {
                    snippet_id,
                    final_score: fused_score,
                    file_path: meta.file_path.clone(),
                    symbol_name: meta.symbol_name.clone(),
                    symbol_type: String::new(),
                    language: String::new(),
                    line_start: meta.line_start,
                    line_end: meta.line_end,
                    code_block: meta.code_block.clone(),
                    source,
                })
            })
            .collect()
    }

    /// Get the current config.
    pub fn config(&self) -> &HybridSearchConfig {
        &self.config
    }
}
