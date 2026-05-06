use axum::{
    extract::{State, Query},
    response::{Json, Html},
    http::StatusCode,
};
use std::sync::Arc;
use crate::storage::StorageManager;
use crate::services::CodeAnalyzer;
use super::models::*;
use md5;
use uuid;
use serde_json::json;
use notify::Watcher;

pub mod vectorize;

pub use vectorize::{semantic_search, query_indexing_status, trigger_embedding_build};

pub async fn query_call_graph(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<QueryCallGraphRequest>,
) -> Result<Json<ApiResponse<QueryCallGraphResponse>>, StatusCode> {
    // Extract request parameters
    let filepath = request.filepath;
    let function_name = request.function_name;
    let max_depth = request.max_depth.unwrap_or(2); // Default max depth is 2
    
    // Retrieve a graph from the in-memory cache populated by init/build_graph
    let graph = storage.get_graph_clone().ok_or(StatusCode::NOT_FOUND)?;
    
    // Debug: Log graph information
    tracing::info!("Loaded graph with {} functions", graph.get_stats().total_functions);
    
    let mut functions = Vec::new();
    
    if let Some(func_name) = function_name {
        // Query specific function by name
        let matching_functions = graph.find_functions_by_name(&func_name);
        
        tracing::info!("Found {} functions matching name '{}'", matching_functions.len(), func_name);
        
        for function in matching_functions {
            tracing::info!("Processing function: {} (ID: {})", function.name, function.id);
            
            // Debug: Log function-specific debug info
            if let Some(func) = graph.get_function_by_id(&function.id) {
                tracing::debug!("Function debug info: {} at {}:{}", func.name, func.file_path.display(), func.line_start);
            }
            
            let callers = graph.get_callers(&function.id);
            let callees = graph.get_callees(&function.id);
            
            tracing::info!("Function {} has {} callers and {} callees", function.name, callers.len(), callees.len());
            
            // Convert to API response format
            let api_function = super::models::FunctionInfo {
                id: function.id.to_string(),
                name: function.name.clone(),
                line_start: function.line_start,
                line_end: function.line_end,
                callers: callers.iter().map(|(caller_func, _relation)| {
                    super::models::CallRelation {
                        function_name: caller_func.name.clone(),
                        file_path: caller_func.file_path.display().to_string(),
                    }
                }).collect(),
                callees: callees.iter().map(|(callee_func, _relation)| {
                    super::models::CallRelation {
                        function_name: callee_func.name.clone(),
                        file_path: callee_func.file_path.display().to_string(),
                    }
                }).collect(),
            };
            
            functions.push(api_function);
        }
    } else {
        // Query all functions in the specified file
        let file_path = std::path::PathBuf::from(&filepath);
        let file_functions = graph.find_functions_by_file(&file_path);
        
        tracing::info!("Found {} functions in file '{}'", file_functions.len(), filepath);
        
        for function in file_functions {
            tracing::info!("Processing function: {} (ID: {})", function.name, function.id);
            
            // Debug: Log function-specific debug info
            if let Some(func) = graph.get_function_by_id(&function.id) {
                tracing::debug!("Function debug info: {} at {}:{}", func.name, func.file_path.display(), func.line_start);
            }
            
            let callers = graph.get_callers(&function.id);
            let callees = graph.get_callees(&function.id);
            
            tracing::info!("Function {} has {} callers and {} callees", function.name, callers.len(), callees.len());
            
            // Convert to API response format
            let api_function = super::models::FunctionInfo {
                id: function.id.to_string(),
                name: function.name.clone(),
                line_start: function.line_start,
                line_end: function.line_end,
                callers: callers.iter().map(|(caller_func, _relation)| {
                    super::models::CallRelation {
                        function_name: caller_func.name.clone(),
                        file_path: caller_func.file_path.display().to_string(),
                    }
                }).collect(),
                callees: callees.iter().map(|(callee_func, _relation)| {
                    super::models::CallRelation {
                        function_name: callee_func.name.clone(),
                        file_path: callee_func.file_path.display().to_string(),
                    }
                }).collect(),
            };
            
            functions.push(api_function);
        }
    }
    
    // If max_depth > 1, expand the call chains
    if max_depth > 1 {
        let mut expanded_functions = functions.clone();
        
        for function in &functions {
            // Expand callers chain
            let mut visited = std::collections::HashSet::new();
            expand_call_chain(&graph, &function.id, &mut visited, &mut expanded_functions, max_depth - 1, true);
            
            // Expand callees chain
            let mut visited = std::collections::HashSet::new();
            expand_call_chain(&graph, &function.id, &mut visited, &mut expanded_functions, max_depth - 1, false);
        }
        
        functions = expanded_functions;
    }
    
    let response = QueryCallGraphResponse {
        filepath,
        functions,
    };
    
    Ok(Json(ApiResponse {
        success: true,
        data: response,
    }))
}

/// Helper function to expand call chains recursively
fn expand_call_chain(
    graph: &crate::codegraph::types::PetCodeGraph,
    function_id: &str,
    visited: &mut std::collections::HashSet<String>,
    functions: &mut Vec<super::models::FunctionInfo>,
    depth: usize,
    is_caller: bool,
) {
    if depth == 0 || visited.contains(function_id) {
        return;
    }
    
    visited.insert(function_id.to_string());
    
    // Parse UUID from string
    let uuid = match uuid::Uuid::parse_str(function_id) {
        Ok(uuid) => uuid,
        Err(_) => return,
    };
    
    let relations = if is_caller {
        graph.get_callers(&uuid)
    } else {
        graph.get_callees(&uuid)
    };
    
    for (related_func, _relation) in relations {
        // Check if we already have this function in our list
        let existing_function = functions.iter_mut().find(|f| f.id == related_func.id.to_string());
        
        if let Some(existing_function) = existing_function {
            // Update existing function with new relations
            if is_caller {
                // Add caller relation
                let caller_relation = super::models::CallRelation {
                    function_name: related_func.name.clone(),
                    file_path: related_func.file_path.display().to_string(),
                };
                
                if !existing_function.callers.iter().any(|c| c.function_name == caller_relation.function_name) {
                    existing_function.callers.push(caller_relation);
                }
            } else {
                // Add callee relation
                let callee_relation = super::models::CallRelation {
                    function_name: related_func.name.clone(),
                    file_path: related_func.file_path.display().to_string(),
                };
                
                if !existing_function.callees.iter().any(|c| c.function_name == callee_relation.function_name) {
                    existing_function.callees.push(callee_relation);
                }
            }
        } else {
            // Create new function entry
            let mut new_function = super::models::FunctionInfo {
                id: related_func.id.to_string(),
                name: related_func.name.clone(),
                line_start: related_func.line_start,
                line_end: related_func.line_end,
                callers: Vec::new(),
                callees: Vec::new(),
            };
            
            if is_caller {
                // Add caller relation
                new_function.callers.push(super::models::CallRelation {
                    function_name: related_func.name.clone(),
                    file_path: related_func.file_path.display().to_string(),
                });
            } else {
                // Add callee relation
                new_function.callees.push(super::models::CallRelation {
                    function_name: related_func.name.clone(),
                    file_path: related_func.file_path.display().to_string(),
                });
            }
            
            functions.push(new_function);
        }
        
        // Recursively expand this function's relations
        expand_call_chain(graph, &related_func.id.to_string(), visited, functions, depth - 1, is_caller);
    }
}

/// New handler for hierarchical tree structure output
pub async fn query_hierarchical_graph(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<super::models::QueryHierarchicalGraphRequest>,
) -> Result<Json<ApiResponse<super::models::QueryHierarchicalGraphResponse>>, StatusCode> {
    let max_depth = request.max_depth.unwrap_or(2);
    let include_file_info = request.include_file_info.unwrap_or(true);

    // 使用内存中的图谱
    let graph = storage.get_graph_clone().ok_or(StatusCode::NOT_FOUND)?;

    let project_id = request.project_id.unwrap_or_else(|| {
        storage.get_current_repo().unwrap_or_default()
    });
    
    let stats = graph.get_stats();
    let total_functions = stats.total_functions;
    let total_relations = stats.resolved_calls + stats.unresolved_calls;
    
    // Build hierarchical tree structure
    let tree_structure = if let Some(root_func_name) = &request.root_function {
        // Start from specific function
        build_hierarchical_tree_from_function(&graph, root_func_name, max_depth, include_file_info)
            .unwrap_or_else(|| create_default_tree_structure(&graph, include_file_info))
    } else {
        // Create default tree structure starting from main functions
        create_default_tree_structure(&graph, include_file_info)
    };
    
    let response = super::models::QueryHierarchicalGraphResponse {
        project_id,
        root_function: request.root_function.clone(),
        max_depth,
        tree_structure,
        total_functions,
        total_relations,
    };
    
    Ok(Json(ApiResponse {
        success: true,
        data: response,
    }))
}

/// Helper function to build hierarchical tree starting from a specific function
fn build_hierarchical_tree_from_function(
    graph: &crate::codegraph::types::PetCodeGraph,
    function_name: &str,
    max_depth: usize,
    include_file_info: bool,
) -> Option<super::models::HierarchicalNode> {
    // Find the function by name
    let functions = graph.find_functions_by_name(function_name);
    if functions.is_empty() {
        return None;
    }
    
    let root_function = &functions[0]; // Use the first match
    
    let mut visited = std::collections::HashSet::new();
    Some(build_hierarchical_node(
        graph,
        root_function,
        max_depth,
        0,
        &mut visited,
        include_file_info,
    ))
}

/// Helper function to create default tree structure
fn create_default_tree_structure(
    graph: &crate::codegraph::types::PetCodeGraph,
    _include_file_info: bool,
) -> super::models::HierarchicalNode {
    let _stats = graph.get_stats();
    
    // Create a root node that contains all functions
    let mut root_node = super::models::HierarchicalNode {
        name: "Project Functions".to_string(),
        function_id: None,
        file_path: None,
        line_start: None,
        line_end: None,
        children: Vec::new(),
        call_type: None,
    };
    
    // Group functions by file for better organization
    let mut file_groups: std::collections::HashMap<String, Vec<_>> = std::collections::HashMap::new();
    
    for function in graph.get_all_functions() {
        let file_path = function.file_path.display().to_string();
        file_groups.entry(file_path).or_insert_with(Vec::new).push(function);
    }
    
    // Create file-level nodes
    for (file_path, functions) in file_groups {
        let mut file_node = super::models::HierarchicalNode {
            name: format!("📁 {}", std::path::Path::new(&file_path).file_name().unwrap_or_default().to_string_lossy()),
            function_id: None,
            file_path: Some(file_path.clone()),
            line_start: None,
            line_end: None,
            children: Vec::new(),
            call_type: None,
        };
        
        // Add functions to file node
        for function in functions {
            let function_node = super::models::HierarchicalNode {
                name: function.name.clone(),
                function_id: Some(function.id.to_string()),
                file_path: Some(function.file_path.display().to_string()),
                line_start: Some(function.line_start),
                line_end: Some(function.line_end),
                children: Vec::new(),
                call_type: Some("function".to_string()),
            };
            
            file_node.children.push(function_node);
        }
        
        root_node.children.push(file_node);
    }
    
    root_node
}

/// Recursive function to build hierarchical node structure
fn build_hierarchical_node(
    graph: &crate::codegraph::types::PetCodeGraph,
    function: &crate::codegraph::types::FunctionInfo,
    max_depth: usize,
    current_depth: usize,
    visited: &mut std::collections::HashSet<String>,
    include_file_info: bool,
) -> super::models::HierarchicalNode {
    if current_depth >= max_depth || visited.contains(&function.id.to_string()) {
        return super::models::HierarchicalNode {
            name: format!("{} (max depth reached)", function.name),
            function_id: Some(function.id.to_string()),
            file_path: if include_file_info { Some(function.file_path.display().to_string()) } else { None },
            line_start: if include_file_info { Some(function.line_start) } else { None },
            line_end: if include_file_info { Some(function.line_end) } else { None },
            children: Vec::new(),
            call_type: Some("max_depth".to_string()),
        };
    }
    
    visited.insert(function.id.to_string());
    
    // Get callees (functions called by this function)
    let callees = graph.get_callees(&function.id);
    
    let mut children = Vec::new();
    
    for (callee_func, _relation) in callees {
        let child_node = build_hierarchical_node(
            graph,
            callee_func,
            max_depth,
            current_depth + 1,
            visited,
            include_file_info,
        );
        children.push(child_node);
    }
    
    super::models::HierarchicalNode {
        name: function.name.clone(),
        function_id: Some(function.id.to_string()),
        file_path: if include_file_info { Some(function.file_path.display().to_string()) } else { None },
        line_start: if include_file_info { Some(function.line_start) } else { None },
        line_end: if include_file_info { Some(function.line_end) } else { None },
        children,
        call_type: Some("function".to_string()),
    }
}

pub async fn query_code_snippet(
    State(storage): State<Arc<StorageManager>>,
    Json(request): Json<QueryCodeSnippetRequest>,
) -> Result<Json<ApiResponse<CodeSnippetResponse>>, StatusCode> {
    // 使用内存中的图谱
    let graph = storage.get_graph_clone().ok_or(StatusCode::NOT_FOUND)?;
    
    // Find the target function
    let target_function = if let Some(func_name) = &request.function_name {
        // Query specific function by name
        let matching_functions = graph.find_functions_by_name(func_name);
        if matching_functions.is_empty() {
            return Err(StatusCode::NOT_FOUND);
        }
        // For now, take the first matching function
        // In a real implementation, you might want to handle multiple matches
        matching_functions[0]
    } else {
        // Query all functions in the specified file and take the first one
        let file_path = std::path::PathBuf::from(&request.filepath);
        let file_functions = graph.find_functions_by_file(&file_path);
        if file_functions.is_empty() {
            return Err(StatusCode::NOT_FOUND);
        }
        file_functions[0]
    };
    
    // Read the file contents
    let file_contents = match std::fs::read_to_string(&target_function.file_path) {
        Ok(contents) => contents,
        Err(e) => {
            tracing::error!("Failed to read file {}: {}", target_function.file_path.display(), e);
            return Err(StatusCode::INTERNAL_SERVER_ERROR);
        }
    };
    
    // Split file into lines
    let lines: Vec<&str> = file_contents.lines().collect();
    
    // Calculate line range for the snippet
    let context_lines = request.context_lines.unwrap_or(3);
    let include_context = request.include_context.unwrap_or(true);
    
    let (line_start, line_end) = if include_context {
        let start = target_function.line_start.saturating_sub(context_lines);
        let end = (target_function.line_end + context_lines).min(lines.len());
        (start, end)
    } else {
        (target_function.line_start, target_function.line_end)
    };
    
    // Extract the code snippet
    let code_snippet = if line_start < lines.len() && line_end <= lines.len() && line_start < line_end {
        lines[line_start..line_end].join("\n")
    } else {
        // Fallback: return the entire function range
        if target_function.line_start < lines.len() && target_function.line_end <= lines.len() {
            lines[target_function.line_start..target_function.line_end].join("\n")
        } else {
            "// Function not found in file".to_string()
        }
    };
    
    // Determine language from file extension
    let language: String = target_function.file_path
        .extension()
        .and_then(|ext| ext.to_str())
        .map(|ext| match ext.to_lowercase().as_str() {
            "rs" => "rust",
            "py" => "python",
            "js" => "javascript",
            "ts" => "typescript",
            "java" => "java",
            "cpp" | "cc" | "cxx" => "cpp",
            "c" => "c",
            "go" => "go",
            "php" => "php",
            "rb" => "ruby",
            "swift" => "swift",
            "kt" => "kotlin",
            "scala" => "scala",
            "cs" => "csharp",
            _ => "unknown"
        })
        .map(|s| s.to_string())
        .unwrap_or_else(|| "unknown".to_string());
    
    let response = CodeSnippetResponse {
        filepath: target_function.file_path.display().to_string(),
        function_name: Some(target_function.name.clone()),
        code_snippet,
        line_start: target_function.line_start,
        line_end: target_function.line_end,
        language,
    };
    
    Ok(Json(ApiResponse {
        success: true,
        data: response,
    }))
} 

pub async fn query_code_skeleton(
    State(_storage): State<Arc<StorageManager>>,
    Json(request): Json<QueryCodeSkeletonRequest>,
) -> Result<Json<ApiResponse<CodeSkeletonBatchResponse>>, StatusCode> {
    let mut skeletons = Vec::new();

    for filepath in &request.filepaths {
        // Read file contents
        let path = std::path::PathBuf::from(filepath);
        let code = match std::fs::read_to_string(&path) {
            Ok(c) => c,
            Err(_) => {
                // Skip files that can't be read, but continue processing others
                tracing::warn!("Failed to read file: {}", filepath);
                continue;
            }
        };

        // Get parser and language
        let (mut parser, language_id) = match crate::codegraph::treesitter::parsers::get_ast_parser_by_filename(&path) {
            Ok(v) => v,
            Err(_) => {
                // Skip files that can't be parsed, but continue processing others
                tracing::warn!("Failed to get parser for file: {}", filepath);
                continue;
            }
        };

        // Parse and build symbol maps
        let symbols = parser.parse(&code, &path);
        let symbols_struct: Vec<crate::codegraph::treesitter::ast_instance_structs::SymbolInformation> =
            symbols.iter().map(|s| s.read().symbol_info_struct()).collect();

        // Build guid maps similar to tests
        use uuid::Uuid;
        use std::collections::HashMap;
        let guid_to_children: HashMap<Uuid, Vec<Uuid>> = symbols
            .iter()
            .map(|s| (s.read().guid().clone(), s.read().childs_guid().clone()))
            .collect();

        // Build a minimal FileASTMarkup-compatible list
        let ast_markup = crate::codegraph::treesitter::file_ast_markup::FileASTMarkup {
            symbols_sorted_by_path_len: symbols_struct.clone(),
        };
        let guid_to_info: HashMap<Uuid, &crate::codegraph::treesitter::ast_instance_structs::SymbolInformation> =
            ast_markup
                .symbols_sorted_by_path_len
                .iter()
                .map(|s| (s.guid.clone(), s))
                .collect();

        // Make formatter
        let formatter = crate::codegraph::treesitter::skeletonizer::make_formatter(&language_id);

        // Filter top-level struct/class and function symbols and build skeleton text
        use crate::codegraph::treesitter::structs::SymbolType;
        let class_symbols: Vec<_> = ast_markup
            .symbols_sorted_by_path_len
            .iter()
            .filter(|x| x.symbol_type == SymbolType::StructDeclaration || x.symbol_type == SymbolType::FunctionDeclaration)
            .collect();

        let mut lines: Vec<String> = Vec::new();
        for symbol in class_symbols {
            let skeleton_line = formatter.make_skeleton(&symbol, &code.to_string(), &guid_to_children, &guid_to_info);
            lines.push(skeleton_line);
        }

        let skeleton_text = if lines.is_empty() {
            String::new()
        } else {
            lines.join("\n\n")
        };

        let language = language_id.to_string();

        let skeleton_response = CodeSkeletonResponse {
            filepath: path.display().to_string(),
            language,
            skeleton_text,
        };

        skeletons.push(skeleton_response);
    }

    let response = CodeSkeletonBatchResponse {
        skeletons,
    };

    Ok(Json(ApiResponse {
        success: true,
        data: response,
    }))
} 

pub async fn draw_call_graph(
    State(storage): State<Arc<StorageManager>>,
    Query(query): Query<super::models::DrawCallGraphQuery>,
) -> Result<Html<String>, StatusCode> {
    // Check if we have the required parameters
    if query.filepath.is_empty() {
        return Ok(Html(generate_main_page_html()));
    }
    
    // First, get the call graph data using existing logic
    let call_graph_request = super::models::QueryCallGraphRequest {
        filepath: query.filepath.clone(),
        function_name: query.function_name.clone(),
        max_depth: query.max_depth,
    };
    
    match query_call_graph(State(storage.clone()), Json(call_graph_request)).await {
        Ok(resp) => {
            let call_graph_data = resp.0.data;
            let html_content = generate_echarts_call_graph_html(&call_graph_data);
            Ok(Html(html_content))
        }
        Err(status) => {
            let html = generate_error_page_html(
                &query.filepath,
                query.function_name.as_deref().unwrap_or(""),
                status,
            );
            Ok(Html(html))
        }
    }
}

fn generate_error_page_html(filepath: &str, function_name: &str, status: axum::http::StatusCode) -> String {
    let title = "Function Call Graph - Error";
    let status_text = format!("{} {}", status.as_u16(), status.canonical_reason().unwrap_or("Error"));
    let suggestion = if status == axum::http::StatusCode::NOT_FOUND {
        "Graph data not found.".to_string()
    } else {
        "An error occurred while generating the call graph. Please check server logs.".to_string()
    };

    let mut html = include_str!("templates/error_page.html").to_string();
    html = html.replace("__TITLE__", title);
    html = html.replace("__STATUS_TEXT__", &status_text);
    html = html.replace("__SUGGESTION__", &suggestion);
    html = html.replace("__FILEPATH__", filepath);
    html = html.replace("__FUNCTION_NAME__", function_name);
    html
}

// 新增：处理根路径的主页
pub async fn draw_call_graph_home() -> Html<String> {
    Html(generate_main_page_html())
}

fn generate_main_page_html() -> String {
    include_str!("templates/main_page.html").to_string()
}


fn generate_echarts_call_graph_html(call_graph_data: &super::models::QueryCallGraphResponse) -> String {
    // Prepare nodes with names and metadata (use function name for link resolution)
    let mut nodes: Vec<serde_json::Value> = Vec::new();
    let mut name_set: std::collections::HashSet<String> = std::collections::HashSet::new();

    for function in &call_graph_data.functions {
        name_set.insert(function.name.clone());
        nodes.push(json!({
            "id": function.name,
            "name": function.name,
            "file_path": call_graph_data.filepath,
            "line_start": function.line_start,
            "line_end": function.line_end
        }));
    }

    // Build links using function names (ECharts allows source/target by name)
    let mut links: Vec<serde_json::Value> = Vec::new();
    for function in &call_graph_data.functions {
        // callees: function -> callee
        for callee in &function.callees {
            if name_set.contains(&callee.function_name) {
                links.push(json!({
                    "source": function.name,
                    "target": callee.function_name,
                    "type": "calls"
                }));
            }
        }
        // callers: caller -> function
        for caller in &function.callers {
            if name_set.contains(&caller.function_name) {
                links.push(json!({
                    "source": caller.function_name,
                    "target": function.name,
                    "type": "called_by"
                }));
            }
        }
    }

    let graph_data = json!({
        "nodes": nodes,
        "links": links
    });

    // Load template and replace placeholders
    let mut html = include_str!("templates/echarts_call_graph.html").to_string();
    html = html.replace("__FILEPATH_INPUT__", &call_graph_data.filepath);
    let fn_input = call_graph_data
        .functions
        .first()
        .map(|f| f.name.clone())
        .unwrap_or_else(|| "All functions".to_string());
    html = html.replace("__FUNCTION_NAME_INPUT__", &fn_input);
    html = html.replace("__GRAPH_JSON__", &serde_json::to_string(&graph_data).unwrap());

    html
} 

pub(crate) async fn perform_analysis(
    storage: Arc<StorageManager>,
    project_dir: std::path::PathBuf,
    project_id: String,
) -> Result<InitResponse, StatusCode> {
    let storage_clone = storage.clone();
    let project_dir_clone = project_dir.clone();
    let project_id_clone = project_id.clone();

    let result = tokio::task::spawn_blocking(move || {
        let mut analyzer = CodeAnalyzer::new();
        match analyzer.analyze_directory(&project_dir_clone) {
            Ok(cg) => {
                let stats = cg.get_stats();

                // Convert to PetCodeGraph
                let mut pet_graph = crate::codegraph::types::PetCodeGraph::new();
                for function in cg.functions.values() {
                    pet_graph.add_function(function.clone());
                }
                for relation in &cg.call_relations {
                    if let Err(e) = pet_graph.add_call_relation(relation.clone()) {
                        tracing::warn!("Failed to add call relation: {}", e);
                    }
                }
                pet_graph.update_stats();

                if let Err(e) = storage_clone.get_persistence().save_graph(&project_id_clone, &pet_graph) {
                    tracing::error!("Failed to save graph: {}", e);
                    return Err(StatusCode::INTERNAL_SERVER_ERROR);
                }

                // Register this project as parsed for later querying
                if let Err(e) = storage_clone.get_persistence().register_project(&project_id_clone, project_dir_clone.to_str().unwrap_or_default()) {
                    tracing::warn!("Failed to register project in registry: {}", e);
                }

                // Cache in memory
                storage_clone.set_graph(pet_graph);

                let resp = InitResponse {
                    project_id: project_id_clone,
                    loaded_from_cache: false,
                    total_functions: stats.total_functions,
                    total_files: stats.total_files,
                };

                Ok(resp)
            }
            Err(e) => {
                tracing::error!("Failed to analyze directory: {}", e);
                Err(StatusCode::INTERNAL_SERVER_ERROR)
            }
        }
    }).await;

    match result {
        Ok(res) => res,
        Err(_) => Err(StatusCode::INTERNAL_SERVER_ERROR),
    }
}

pub(crate) fn setup_watcher(
    storage: Arc<StorageManager>,
    project_dir: std::path::PathBuf,
    project_id: String,
) {
    if storage.has_watcher(&project_id) {
        return;
    }

    let storage_clone = storage.clone();
    let project_dir_clone = project_dir.clone();
    let project_id_clone = project_id.clone();
    let runtime_handle = tokio::runtime::Handle::current();

    // Create a channel for debounce signals
    let (tx, mut rx) = tokio::sync::mpsc::unbounded_channel();

    // Spawn the debouncer task
    runtime_handle.spawn(async move {
        // Wait 20s to prevent frequent re-analysis
        const DEBOUNCE_DURATION: std::time::Duration = std::time::Duration::from_secs(20);

        loop {
            // Wait for the first change event
            if rx.recv().await.is_none() {
                break; // Channel closed
            }

            // tracing::info!("File change detected, starting debounce timer (20s)");

            // Debounce logic
            let mut last_change = std::time::Instant::now();
            loop {
                let deadline = tokio::time::Instant::from_std(last_change + DEBOUNCE_DURATION);
                let timeout = tokio::time::sleep_until(deadline);

                tokio::select! {
                    _ = timeout => {
                        // Timer expired without new events
                        break;
                    }
                    msg = rx.recv() => {
                        match msg {
                            Some(_) => {
                                // New event received, reset timer
                                last_change = std::time::Instant::now();
                            }
                            None => return, // Channel closed
                        }
                    }
                }
            }

            // Debounce finished, trigger analysis
            tracing::info!("Debounce complete, triggering re-analysis");
            let storage = storage_clone.clone();
            let dir = project_dir_clone.clone();
            let id = project_id_clone.clone();

            if let Err(e) = perform_analysis(storage, dir, id).await {
                tracing::error!("Re-analysis failed: {:?}", e);
            } else {
                tracing::info!("Re-analysis completed successfully");
            }
            // Trigger embedding build
            let repo_path = project_dir_clone.to_string_lossy().to_string();
            tracing::info!("Triggering embedding rebuild for {}", repo_path);
            if let Err(e) = vectorize::trigger_embedding_build(storage_clone.clone(), repo_path).await {
                    tracing::info!("Embedding build skipped: {}", e);
            }
        }
    });

    let watcher_res = notify::recommended_watcher(move |res: Result<notify::Event, notify::Error>| {
        match res {
            Ok(event) => {
                if event.kind.is_modify() || event.kind.is_create() || event.kind.is_remove() {
                     tracing::info!("File change detected: {:?}, queueing re-analysis", event.paths);
                     let _ = tx.send(());
                }
            }
            Err(e) => tracing::error!("Watch error: {:?}", e),
        }
    });

    match watcher_res {
        Ok(mut watcher) => {
             if let Err(e) = watcher.watch(&project_dir, notify::RecursiveMode::Recursive) {
                tracing::error!("Failed to start watcher: {:?}", e);
            } else {
                storage.add_watcher(project_id, watcher);
                tracing::info!("Started watching project: {}", project_dir.display());
            }
        }
        Err(e) => tracing::error!("Failed to create watcher: {:?}", e),
    }
}

pub async fn investigate_repo(
    State(storage): State<Arc<StorageManager>>,
    _: Json<super::models::InvestigateRepoRequest>,
) -> Result<Json<ApiResponse<super::models::InvestigateRepoResponse>>, StatusCode> {
    // 使用内存中的图谱和当前绑定的仓库
    let graph = storage.get_graph_clone().ok_or(StatusCode::NOT_FOUND)?;
    let repo_path = storage.get_current_repo().ok_or(StatusCode::NOT_FOUND)?;
    let project_id = format!("{:x}", md5::compute(&repo_path)); 

	// Compute out-degree for each function and collect top 15
	use std::cmp::Reverse;
	let mut items: Vec<(usize, uuid::Uuid)> = Vec::new();
	for (func_id, _node_idx) in graph.function_to_node.iter() {
		let out_degree = graph.get_callees(func_id).len();
		items.push((out_degree, *func_id));
	}
	items.sort_by_key(|(deg, _)| Reverse(*deg));
	let top = items.into_iter().take(15).collect::<Vec<_>>();

	use std::collections::BTreeSet;
	
	// Helper function to build directory tree
	fn build_directory_tree(root_path: &std::path::Path) -> std::io::Result<String> {
		// List of directories to ignore
		let ignored_dirs = [
			"node_modules",
			"target",
			"dist",
			"build",
			"vendor",
			".git",
			".svn",
			".hg",
			".vscode",
			".idea",
			".DS_Store",
			"__pycache__",
			".pytest_cache",
			"coverage",
			".nyc_output",
			".sass-cache",
			".yarn",
			".pnpm",
			"go/pkg",
			"go/bin",
		];
		
		fn is_ignored(name: &str, ignored_dirs: &[&str]) -> bool {
			// Ignore hidden files/directories (starting with .)
			if name.starts_with('.') {
				return true;
			}
			
			// Ignore specific directories
			ignored_dirs.contains(&name)
		}
		
		// Recursive function to build tree structure
		fn build_tree_recursive(
			path: &std::path::Path,
			prefix: &str,
			ignored_dirs: &[&str],
			is_ignored: fn(&str, &[&str]) -> bool,
		) -> std::io::Result<String> {
			let mut result = String::new();
			
			// If it's a directory, process its contents
			if path.is_dir() {
				let mut entries: Vec<_> = std::fs::read_dir(path)?
					.filter_map(|entry| entry.ok())
					.filter(|entry| {
						let name = entry.file_name();
						let name_str = name.to_string_lossy();
						!is_ignored(&name_str, ignored_dirs)
					})
					.collect();
				
				// Sort entries: directories first, then files, both alphabetically
				entries.sort_by(|a, b| {
					let a_is_dir = a.file_type().map(|ft| ft.is_dir()).unwrap_or(false);
					let b_is_dir = b.file_type().map(|ft| ft.is_dir()).unwrap_or(false);
					
					match (a_is_dir, b_is_dir) {
						(true, false) => std::cmp::Ordering::Less,
						(false, true) => std::cmp::Ordering::Greater,
						_ => {
							let a_name = a.file_name().to_string_lossy().to_string();
							let b_name = b.file_name().to_string_lossy().to_string();
							a_name.cmp(&b_name)
						}
					}
				});
				
				// Process each entry
				let count = entries.len();
				for (i, entry) in entries.iter().enumerate() {
					let is_last_entry = i == count - 1;
					
					// Get file name
					let file_name = entry.file_name()
						.to_string_lossy()
						.to_string();
					
					// Add current item to result
					let connector = if is_last_entry { "└── " } else { "├── " };
					result.push_str(&format!("{}{}{}\n", prefix, connector, file_name));
					
					// If it's a directory, recursively process its contents
					if entry.path().is_dir() {
						let new_prefix = if is_last_entry {
							format!("{}    ", prefix)
						} else {
							format!("{}│   ", prefix)
						};
						
						let entry_result = build_tree_recursive(
							entry.path().as_path(),
							&new_prefix,
							ignored_dirs,
							is_ignored,
						)?;
						
						result.push_str(&entry_result);
					}
				}
			}
			
			Ok(result)
		}
		
		// Start building the tree
		let file_name = root_path.file_name()
			.and_then(|name| name.to_str())
			.unwrap_or("");
		let mut result = String::new();
		result.push_str(&format!("{}\n", file_name));
		let tree_content = build_tree_recursive(root_path, "", &ignored_dirs, is_ignored)?;
		result.push_str(&tree_content);
		Ok(result)
	}
	
	let mut files_needed: BTreeSet<std::path::PathBuf> = BTreeSet::new();
	let mut core_functions: Vec<super::models::InvestigateFunctionInfo> = Vec::new();
	for (out_degree, func_id) in top.iter() {
		if let Some(f) = graph.get_function_by_id(func_id) {
			// Collect callers with deduplication based on function_name + file_path
			let mut callers_set: std::collections::HashSet<(String, String)> = std::collections::HashSet::new();
			let callers = graph.get_callers(func_id)
				.into_iter()
				.filter_map(|(caller, _rel)| {
					let function_name = caller.name.clone();
					let file_path = caller.file_path.display().to_string().replace(&repo_path, "").trim_start_matches('/').to_string();
					let key = (function_name.clone(), file_path.clone());
					if callers_set.insert(key) {
						Some(super::models::CallRelation {
							function_name,
							file_path,
						})
					} else {
						None
					}
				})
				.collect();

			// Collect callees with deduplication based on function_name + file_path
			let mut callees_set: std::collections::HashSet<(String, String)> = std::collections::HashSet::new();
			let callees = graph.get_callees(func_id)
				.into_iter()
				.filter_map(|(callee, _rel)| {
					let function_name = callee.name.clone();
					let file_path = callee.file_path.display().to_string().replace(&repo_path, "").trim_start_matches('/').to_string();
					let key = (function_name.clone(), file_path.clone());
					if callees_set.insert(key) {
						Some(super::models::CallRelation {
							function_name,
							file_path,
						})
					} else {
						None
					}
				})
				.collect();

			core_functions.push(super::models::InvestigateFunctionInfo {
				name: f.name.clone(),
				file_path: f.file_path.display().to_string().replace(&repo_path, "").trim_start_matches('/').to_string(),
				out_degree: *out_degree,
				callers,
				callees,
			});
			files_needed.insert(f.file_path.clone());
		}
	}

	// For each unique file, build code skeleton text (reuse skeleton builder logic)
	let mut file_skeletons: Vec<super::models::CodeSkeletonResponse> = Vec::new();
	for path in files_needed.into_iter() {
		let rel_path = path.display().to_string().replace(&repo_path, "").trim_start_matches('/').to_string();
		// Reuse existing batch skeletonizer by calling internal logic inline
		let code = match std::fs::read_to_string(&path) {
			Ok(c) => c,
			Err(_) => continue,
		};
		let (mut parser, language_id) = match crate::codegraph::treesitter::parsers::get_ast_parser_by_filename(&path) {
			Ok(v) => v,
			Err(_) => continue,
		};
		let symbols = parser.parse(&code, &path);
		let symbols_struct: Vec<crate::codegraph::treesitter::ast_instance_structs::SymbolInformation> =
			symbols.iter().map(|s| s.read().symbol_info_struct()).collect();
		use uuid::Uuid;
		use std::collections::HashMap;
		let guid_to_children: HashMap<Uuid, Vec<Uuid>> = symbols
			.iter()
			.map(|s| (s.read().guid().clone(), s.read().childs_guid().clone()))
			.collect();
		let ast_markup = crate::codegraph::treesitter::file_ast_markup::FileASTMarkup {
			symbols_sorted_by_path_len: symbols_struct.clone(),
		};
		let guid_to_info: HashMap<Uuid, &crate::codegraph::treesitter::ast_instance_structs::SymbolInformation> =
			ast_markup
				.symbols_sorted_by_path_len
				.iter()
				.map(|s| (s.guid.clone(), s))
				.collect();
		let formatter = crate::codegraph::treesitter::skeletonizer::make_formatter(&language_id);
		use crate::codegraph::treesitter::structs::SymbolType;
		let class_symbols: Vec<_> = ast_markup
			.symbols_sorted_by_path_len
			.iter()
			.filter(|x| x.symbol_type == SymbolType::StructDeclaration || x.symbol_type == SymbolType::FunctionDeclaration)
			.collect();
		let mut lines: Vec<String> = Vec::new();
		for symbol in class_symbols {
			let skeleton_line = formatter.make_skeleton(&symbol, &code.to_string(), &guid_to_children, &guid_to_info);
			lines.push(skeleton_line);
		}
		let skeleton_text = if lines.is_empty() { String::new() } else { lines.join("\n\n") };
		file_skeletons.push(super::models::CodeSkeletonResponse {
			filepath: rel_path,
			language: language_id.to_string(),
			skeleton_text,
		});
	}

	// Build directory tree
	let repo_path_buf = std::path::Path::new(&repo_path);
	let directory_tree = match build_directory_tree(repo_path_buf) {
		Ok(tree) => tree,
		Err(_) => "".to_string(),
	};

	let stats = graph.get_stats();
	let resp = super::models::InvestigateRepoResponse {
		project_id,
		total_functions: stats.total_functions,
		core_functions,
		file_skeletons,
		directory_tree,
	};

	Ok(Json(ApiResponse { success: true, data: resp }))
}
