use serde::{Deserialize, Serialize};
use crate::config::RerankerConfig;
use crate::storage::traits_bm25::FusedCandidate;
use std::env;
use anyhow::{Result, anyhow};
use tracing::debug;

pub struct RerankerService {
    config: RerankerConfig,
    client: reqwest::Client,
}

#[derive(Debug, Serialize)]
struct RerankRequest {
    model: String,
    query: String,
    documents: Vec<String>,
    top_n: usize,
}

#[derive(Debug, Deserialize)]
struct RerankResponse {
    results: Vec<RerankResultItem>,
}

#[derive(Debug, Deserialize)]
struct RerankResultItem {
    index: usize,
    #[serde(alias = "score")]
    relevance_score: f64,
}

impl RerankerService {
    pub fn new(config: RerankerConfig) -> Self {
        let client = reqwest::Client::builder()
            .timeout(std::time::Duration::from_secs(config.timeout_secs))
            .build()
            .expect("Failed to build reqwest client for RerankerService");
        Self { config, client }
    }

    /// 获取配置的只读引用
    pub fn config(&self) -> &RerankerConfig {
        &self.config
    }

    pub async fn rerank(
        &self,
        query: &str,
        candidates: Vec<FusedCandidate>,
    ) -> Result<Vec<FusedCandidate>> {
        if !self.config.enabled || candidates.is_empty() {
            return Ok(candidates);
        }

        let documents: Vec<String> = candidates.iter().map(|c| c.code_block.clone()).collect();
        let top_n = self.config.top_n.min(candidates.len());

        let request = RerankRequest {
            model: self.config.model.clone(),
            query: query.to_string(),
            documents,
            top_n,
        };

        let api_key = env::var("SILICONFLOW_API_KEY")
            .map_err(|_| anyhow!("SILICONFLOW_API_KEY environment variable not set"))?;

        let url = format!("{}/v1/rerank", self.config.base_url.trim_end_matches('/'));

        debug!("Reranker: calling {} with top_n={}", url, top_n);

        let response = self.client
            .post(&url)
            .header("Authorization", format!("Bearer {}", api_key))
            .header("Content-Type", "application/json")
            .json(&request)
            .send()
            .await
            .map_err(|e| anyhow!("Reranker HTTP request failed: {}", e))?;

        let status = response.status();
        if !status.is_success() {
            let text = response.text().await.unwrap_or_default();
            return Err(anyhow!("Reranker API returned error status {}: {}", status, text));
        }

        let rerank_resp: RerankResponse = response
            .json()
            .await
            .map_err(|e| anyhow!("Reranker response parse failed: {}", e))?;

        let mut reranked: Vec<FusedCandidate> = rerank_resp
            .results
            .into_iter()
            .filter_map(|item| {
                candidates.get(item.index).map(|orig| {
                    let mut c = orig.clone();
                    c.final_score = item.relevance_score;
                    c
                })
            })
            .collect();

        // 确保按分数降序排列
        reranked.sort_by(|a, b| {
            b.final_score
                .partial_cmp(&a.final_score)
                .unwrap_or(std::cmp::Ordering::Equal)
        });

        debug!("Reranker: reranked {} candidates", reranked.len());
        Ok(reranked)
    }
}
