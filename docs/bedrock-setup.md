# AWS Bedrock Integration Setup Guide

This guide explains how to configure and use AWS Bedrock models with the DeepCoder assistant.

## Prerequisites

1. **AWS Account**: You need an AWS account with Bedrock access
2. **AWS CLI**: Install and configure AWS CLI with appropriate credentials
3. **Model Access**: Request access to the Bedrock models you want to use in the AWS Console

## Configuration

### 1. AWS Credentials Setup

You can configure AWS credentials in several ways:

#### Option 1: Environment Variables
```bash
export AWS_ACCESS_KEY_ID=your_access_key
export AWS_SECRET_ACCESS_KEY=your_secret_key
export AWS_REGION=us-east-1
```

#### Option 2: AWS CLI Configuration
```bash
aws configure
# Follow prompts to enter your credentials
```

#### Option 3: AWS Profile (Recommended)
```bash
aws configure --profile bedrock-user
# Configure with a specific profile name
```

### 2. Model Configuration

Add Bedrock configuration to your `config/config.toml`:

```toml
[llm]
use_provider = "bedrock"

[llm.providers.bedrock]
model = "us.anthropic.claude-3-7-sonnet-20250219-v1:0"
temperature = 0.0
max_tokens = 3000
aws_region = "us-east-1"
# aws_profile = "bedrock-user"  # Optional: specify AWS profile
# model_provider = "anthropic"  # Optional: explicit provider (auto-detected)
```

### 3. Supported Bedrock Models

The system automatically detects the provider from the model ID. Here are some examples:

#### Anthropic Claude Models
```toml
model = "us.anthropic.claude-3-7-sonnet-20250219-v1:0"
model = "us.anthropic.claude-3-5-sonnet-20241022-v2:0"
model = "us.anthropic.claude-3-haiku-20240307-v1:0"
```

#### Amazon Nova Models
```toml
model = "us.amazon.nova-lite-v1:0"
model = "us.amazon.nova-pro-v1:0"
```

#### Meta Llama Models
```toml
model = "us.meta.llama3-1-405b-instruct-v1:0"
model = "us.meta.llama3-1-70b-instruct-v1:0"
```

#### Cohere Models
```toml
model = "us.cohere.command-r-plus-v1:0"
model = "us.cohere.command-r-v1:0"
```

#### AI21 Models
```toml
model = "us.ai21.jamba-1-5-large-v1:0"
model = "us.ai21.jamba-1-5-mini-v1:0"
```

### 4. Inference Profiles

You can also use inference profiles for cross-region inference:

```toml
# Cross-region inference
model = "us.anthropic.claude-3-7-sonnet-20250219-v1:0"

# Regional inference
model = "us-west-2.anthropic.claude-3-7-sonnet-20250219-v1:0"
```

## Usage Examples

### Basic Usage
```bash
# Set your AWS credentials
export AWS_ACCESS_KEY_ID=your_key
export AWS_SECRET_ACCESS_KEY=your_secret

# Run the assistant with Bedrock
go run main.go
```

### Using AWS Profile
```bash
# Configure AWS profile
aws configure --profile bedrock-user

# Update config.toml to use the profile
# aws_profile = "bedrock-user"

# Run the assistant
go run main.go
```

### Testing Different Models
```bash
# Test with Claude
sed -i 's/use_provider = ".*"/use_provider = "bedrock"/' config/config.toml
sed -i 's/model = ".*"/model = "us.anthropic.claude-3-7-sonnet-20250219-v1:0"/' config/config.toml

# Test with Nova
sed -i 's/model = ".*"/model = "us.amazon.nova-lite-v1:0"/' config/config.toml
```

## Troubleshooting

### Common Issues

1. **Access Denied**: Ensure you have requested access to the Bedrock models in AWS Console
2. **Region Not Supported**: Check if your chosen region supports the model you're trying to use
3. **Credentials Not Found**: Verify your AWS credentials are properly configured
4. **Model Not Available**: Some models may not be available in all regions

### Debugging

Enable verbose logging to see detailed Bedrock interactions:

```bash
export AWS_SDK_LOAD_CONFIG=1
export AWS_REGION=us-east-1
```

### Provider Detection

The system automatically detects the provider from model IDs:
- Models containing `.nova-` → Amazon Nova
- Models containing `anthropic` → Anthropic Claude
- Models containing `meta` → Meta Llama
- Models containing `cohere` → Cohere Command
- Models containing `ai21` → AI21 Jamba

You can override auto-detection by explicitly setting `model_provider` in the configuration.

## Security Best Practices

1. **Use IAM Roles**: For production, use IAM roles instead of access keys
2. **Least Privilege**: Grant only necessary Bedrock permissions
3. **Rotate Credentials**: Regularly rotate your AWS access keys
4. **Use Profiles**: Use AWS profiles to manage different environments
5. **Secure Storage**: Never commit AWS credentials to version control

## Model Capabilities

Different Bedrock models have different capabilities:

- **Claude**: Excellent for coding, analysis, and creative tasks
- **Nova**: Good balance of performance and cost
- **Llama**: Strong open-source alternative
- **Cohere**: Specialized for specific enterprise use cases
- **AI21**: Advanced reasoning and multilingual support

Choose the model that best fits your use case and budget requirements.