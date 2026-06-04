import React, { useState } from 'react';
import { X, Plus, Key, Link, Type, Database, Save, XCircle } from 'lucide-react';

interface AddModelModalProps {
  isOpen: boolean;
  onClose: () => void;
  onAddModel: (model: ModelConfig) => void;
}

export interface ModelConfig {
  name: string;
  provider: string;
  modelId: string;
  apiKey: string;
  baseUrl: string;
}

const PROVIDERS = [
  'OpenAI',
  'Anthropic', 
  'Google',
  'Azure',
  'Local',
  'Custom'
];

export const AddModelModal: React.FC<AddModelModalProps> = ({ 
  isOpen, 
  onClose, 
  onAddModel 
}) => {
  const [formData, setFormData] = useState<ModelConfig>({
    name: '',
    provider: 'OpenAI',
    modelId: '',
    apiKey: '',
    baseUrl: ''
  });

  const [errors, setErrors] = useState<Partial<Record<keyof ModelConfig, string>>>({});

  const handleInputChange = (field: keyof ModelConfig, value: string) => {
    setFormData(prev => ({ ...prev, [field]: value }));
    // 清除对应字段的错误
    if (errors[field]) {
      setErrors(prev => ({ ...prev, [field]: undefined }));
    }
  };

  const validateForm = (): boolean => {
    const newErrors: Partial<Record<keyof ModelConfig, string>> = {};
    
    if (!formData.name.trim()) {
      newErrors.name = '模型名称不能为空';
    }
    
    if (!formData.modelId.trim()) {
      newErrors.modelId = '模型ID不能为空';
    }
    
    if (!formData.apiKey.trim()) {
      newErrors.apiKey = 'API密钥不能为空';
    }
    
    setErrors(newErrors);
    return Object.keys(newErrors).length === 0;
  };

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    
    if (validateForm()) {
      onAddModel(formData);
      // 重置表单
      setFormData({
        name: '',
        provider: 'OpenAI',
        modelId: '',
        apiKey: '',
        baseUrl: ''
      });
      onClose();
    }
  };

  const handleCancel = () => {
    // 重置表单
    setFormData({
      name: '',
      provider: 'OpenAI',
      modelId: '',
      apiKey: '',
      baseUrl: ''
    });
    setErrors({});
    onClose();
  };

  if (!isOpen) return null;

  return (
    <div className="fixed inset-0 bg-black bg-opacity-50 flex items-center justify-center z-50 backdrop-blur-sm">
      <div className="bg-vscode-bg-primary border border-vscode-border rounded-xl shadow-2xl w-full max-w-md mx-4">
        {/* 头部 */}
        <div className="flex items-center justify-between p-4 border-b border-vscode-border">
          <h2 className="text-lg font-semibold text-vscode-text-primary flex items-center gap-2">
            <Plus className="w-5 h-5" />
            添加模型
          </h2>
          <button
            onClick={handleCancel}
            className="p-1 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-secondary rounded-lg transition-colors"
          >
            <X className="w-4 h-4" />
          </button>
        </div>

        {/* 表单 */}
        <form onSubmit={handleSubmit} className="p-4 space-y-4">
          {/* 模型名称 */}
          <div>
            <label className="block text-sm font-medium text-vscode-text-primary mb-2 flex items-center gap-2">
              <Type className="w-4 h-4" />
              模型名称
            </label>
            <input
              type="text"
              value={formData.name}
              onChange={(e) => handleInputChange('name', e.target.value)}
              placeholder="例如: My Custom Model"
              className={`w-full px-3 py-2 bg-vscode-bg-secondary border rounded-lg text-vscode-text-primary placeholder-vscode-text-secondary focus:outline-none focus:ring-2 focus:ring-vscode-accent transition-all ${
                errors.name ? 'border-red-500' : 'border-vscode-border focus:border-vscode-accent'
              }`}
            />
            {errors.name && (
              <p className="mt-1 text-xs text-red-500">{errors.name}</p>
            )}
          </div>

          {/* 提供商 */}
          <div>
            <label className="block text-sm font-medium text-vscode-text-primary mb-2 flex items-center gap-2">
              <Database className="w-4 h-4" />
              提供商
            </label>
            <select
              value={formData.provider}
              onChange={(e) => handleInputChange('provider', e.target.value)}
              className="w-full px-3 py-2 bg-vscode-bg-secondary border border-vscode-border rounded-lg text-vscode-text-primary focus:outline-none focus:ring-2 focus:ring-vscode-accent transition-all"
            >
              {PROVIDERS.map(provider => (
                <option key={provider} value={provider}>
                  {provider}
                </option>
              ))}
            </select>
          </div>

          {/* 模型ID */}
          <div>
            <label className="block text-sm font-medium text-vscode-text-primary mb-2 flex items-center gap-2">
              <Type className="w-4 h-4" />
              模型ID
            </label>
            <input
              type="text"
              value={formData.modelId}
              onChange={(e) => handleInputChange('modelId', e.target.value)}
              placeholder="例如: gpt-4, claude-3-sonnet-20240229"
              className={`w-full px-3 py-2 bg-vscode-bg-secondary border rounded-lg text-vscode-text-primary placeholder-vscode-text-secondary focus:outline-none focus:ring-2 focus:ring-vscode-accent transition-all ${
                errors.modelId ? 'border-red-500' : 'border-vscode-border focus:border-vscode-accent'
              }`}
            />
            {errors.modelId && (
              <p className="mt-1 text-xs text-red-500">{errors.modelId}</p>
            )}
          </div>

          {/* API密钥 */}
          <div>
            <label className="block text-sm font-medium text-vscode-text-primary mb-2 flex items-center gap-2">
              <Key className="w-4 h-4" />
              API密钥
            </label>
            <input
              type="password"
              value={formData.apiKey}
              onChange={(e) => handleInputChange('apiKey', e.target.value)}
              placeholder="输入您的API密钥"
              className={`w-full px-3 py-2 bg-vscode-bg-secondary border rounded-lg text-vscode-text-primary placeholder-vscode-text-secondary focus:outline-none focus:ring-2 focus:ring-vscode-accent transition-all ${
                errors.apiKey ? 'border-red-500' : 'border-vscode-border focus:border-vscode-accent'
              }`}
            />
            {errors.apiKey && (
              <p className="mt-1 text-xs text-red-500">{errors.apiKey}</p>
            )}
          </div>

          {/* Base URL */}
          <div>
            <label className="block text-sm font-medium text-vscode-text-primary mb-2 flex items-center gap-2">
              <Link className="w-4 h-4" />
              Base URL (可选)
            </label>
            <input
              type="text"
              value={formData.baseUrl}
              onChange={(e) => handleInputChange('baseUrl', e.target.value)}
              placeholder="例如: https://api.openai.com/v1"
              className="w-full px-3 py-2 bg-vscode-bg-secondary border border-vscode-border rounded-lg text-vscode-text-primary placeholder-vscode-text-secondary focus:outline-none focus:ring-2 focus:ring-vscode-accent transition-all focus:border-vscode-accent"
            />
          </div>

          {/* 按钮组 */}
          <div className="flex gap-3 pt-4">
            <button
              type="button"
              onClick={handleCancel}
              className="flex-1 flex items-center justify-center gap-2 px-4 py-2 text-vscode-text-secondary hover:text-vscode-text-primary hover:bg-vscode-bg-secondary border border-vscode-border rounded-lg transition-all duration-200"
            >
              <XCircle className="w-4 h-4" />
              取消
            </button>
            <button
              type="submit"
              className="flex-1 flex items-center justify-center gap-2 px-4 py-2 bg-vscode-accent hover:bg-vscode-accent-hover text-white rounded-lg transition-all duration-200"
            >
              <Save className="w-4 h-4" />
              保存
            </button>
          </div>
        </form>
      </div>
    </div>
  );
};