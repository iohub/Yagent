use serde::Deserialize;
use std::fs;
use tracing::info;

fn default_embedding_db_uri() -> String {
    let home = dirs::home_dir().unwrap_or_default();
    home.join(".codeactor/data/embedding")
        .to_string_lossy()
        .to_string()
}

fn default_graph_db_uri() -> String {
    let home = dirs::home_dir().unwrap_or_default();
    home.join(".codeactor/data/graph")
        .to_string_lossy()
        .to_string()
}

#[derive(Debug, Deserialize, Clone)]
pub struct Config {
    pub codebase: CodeBaseConfig,
}

#[derive(Debug, Deserialize, Clone)]
pub struct CodeBaseConfig {
    #[serde(default)]
    pub enable_embedding: bool,
    #[serde(default = "default_embedding_db_uri")]
    pub embedding_db_uri: String,
    #[serde(default = "default_graph_db_uri")]
    pub graph_db_uri: String,
    pub embedding: EmbeddingConfig,
}

#[derive(Debug, Deserialize, Clone)]
pub struct EmbeddingConfig {
    pub model: String,
    pub api_token: String,
    pub api_base_url: String,
    pub dimensions: Option<usize>,
}

impl Config {
    pub fn load() -> Result<Self, Box<dyn std::error::Error>> {
        let home_dir = dirs::home_dir().ok_or("Could not find home directory")?;
        let config_path = home_dir.join(".codeactor/config/config.toml");

        info!("Loading configuration from: {:?}", config_path);

        let contents = fs::read_to_string(&config_path).map_err(|e| {
            format!("Failed to read config file at {:?}: {}", config_path, e)
        })?;

        let config: Config = toml::from_str(&contents).map_err(|e| {
            format!("Failed to parse config file: {}", e)
        })?;

        Ok(config)
    }
}
