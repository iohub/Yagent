use serde::Deserialize;
use std::collections::HashMap;
use std::fs;
use tracing::info;

#[derive(Debug, Deserialize, Clone)]
pub struct Config {
    pub llm: LlmConfig,
    pub app: AppConfig,
    pub agent: AgentConfig,
    pub codebase: CodeBaseConfig,
}

#[derive(Debug, Deserialize, Clone)]
pub struct LlmConfig {
    pub use_provider: String,
    pub providers: HashMap<String, ProviderConfig>,
}

#[derive(Debug, Deserialize, Clone)]
pub struct ProviderConfig {
    pub model: String,
    pub temperature: f32,
    pub max_tokens: usize,
    pub api_base_url: Option<String>,
    pub api_key: Option<String>,
    pub aws_region: Option<String>,
}

#[derive(Debug, Deserialize, Clone)]
pub struct AppConfig {
    pub enable_streaming: bool,
}

#[derive(Debug, Deserialize, Clone)]
pub struct CodeBaseConfig {
    pub enable_embedding: bool,
    pub embedding_db_uri: String,
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


#[derive(Debug, Deserialize, Clone)]
pub struct AgentConfig {
    pub conductor_max_steps: Option<usize>,
    pub coding_max_steps: Option<usize>,
    pub repo_max_steps: Option<usize>,
    pub lang: Option<String>,
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

    pub fn get_current_provider(&self) -> Option<&ProviderConfig> {
        self.llm.providers.get(&self.llm.use_provider)
    }
}
