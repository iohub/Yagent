import React, { useState, useEffect } from 'react';
import { configManager } from '../utils/configManager';

interface SettingsPageFullProps {
  onBack: () => void;
}

interface NavigationItem {
  id: string;
  label: string;
  icon: string;
}

interface DocumentationSource {
  id: string;
  name: string;
  url: string;
  type: 'api' | 'docs' | 'wiki';
}

export const SettingsPageFull: React.FC<SettingsPageFullProps> = ({ onBack }) => {
  const [activeTab, setActiveTab] = useState('indexing');
  const [indexingEnabled, setIndexingEnabled] = useState(true);
  const [indexingProgress, setIndexingProgress] = useState(30);
  const [isIndexing, setIsIndexing] = useState(true);
  const [documentationSources] = useState<DocumentationSource[]>([]);
  const [models, setModels] = useState<any[]>([]);
  const [roles, setRoles] = useState<any[]>([]);
  const [loading, setLoading] = useState(true);

  const navigationItems: NavigationItem[] = [
    { id: 'models', label: 'Models', icon: '◆' },
    { id: 'rules', label: 'Rules', icon: '≡' },
    { id: 'tools', label: 'Tools', icon: '⚙' },
    { id: 'agents', label: 'Agents', icon: '◉' },
    { id: 'indexing', label: 'Indexing', icon: '◈' },
  ];

  const handleAddDocumentation = () => {
    alert('Add documentation source functionality will be implemented soon!');
  };

  const handleRemoveModel = (modelName: string) => {
    if (window.confirm(`Are you sure you want to remove the model "${modelName}"?`)) {
      configManager.removeCustomModel(modelName);
      // Refresh the models list
      const config = configManager.getConfig();
      const customModels = config.customModels || [];
      const defaultModels = [
        { name: 'Claude 3.5 Sonnet', provider: 'Anthropic', contextLength: 200000, isCustom: false },
        { name: 'Claude 3 Opus', provider: 'Anthropic', contextLength: 200000, isCustom: false },
        { name: 'Claude 3 Haiku', provider: 'Anthropic', contextLength: 200000, isCustom: false },
        { name: 'GPT-4', provider: 'OpenAI', contextLength: 128000, isCustom: false },
        { name: 'GPT-4 Turbo', provider: 'OpenAI', contextLength: 128000, isCustom: false },
        { name: 'GPT-3.5 Turbo', provider: 'OpenAI', contextLength: 16385, isCustom: false }
      ];
      
      const allModels = [
        ...defaultModels,
        ...customModels.map((model: any) => ({
          ...model,
          provider: model.provider || 'Custom',
          isCustom: true
        }))
      ];
      
      setModels(allModels);
    }
  };

  // Load configuration on component mount
  useEffect(() => {
    const loadConfig = async () => {
      try {
        setLoading(true);
        
        // Get current configuration
        const config = configManager.getConfig();
        
        // Load models from customModels and add default models
        const customModels = config.customModels || [];
        const defaultModels = [
          { name: 'Claude 3.5 Sonnet', provider: 'Anthropic', contextLength: 200000, isCustom: false },
          { name: 'Claude 3 Opus', provider: 'Anthropic', contextLength: 200000, isCustom: false },
          { name: 'Claude 3 Haiku', provider: 'Anthropic', contextLength: 200000, isCustom: false },
          { name: 'GPT-4', provider: 'OpenAI', contextLength: 128000, isCustom: false },
          { name: 'GPT-4 Turbo', provider: 'OpenAI', contextLength: 128000, isCustom: false },
          { name: 'GPT-3.5 Turbo', provider: 'OpenAI', contextLength: 16385, isCustom: false }
        ];
        
        // Mark custom models
        const allModels = [
          ...defaultModels,
          ...customModels.map((model: any) => ({
            ...model,
            provider: model.provider || 'Custom',
            isCustom: true
          }))
        ];
        
        // Load default roles
        const defaultRoles = [
          { name: 'Developer', description: 'General programming and software development tasks', isCustom: false },
          { name: 'Code Reviewer', description: 'Review code for quality, security, and best practices', isCustom: false },
          { name: 'Architect', description: 'System design and architecture decisions', isCustom: false },
          { name: 'Debugger', description: 'Find and fix bugs and issues', isCustom: false },
          { name: 'Teacher', description: 'Explain concepts and provide educational content', isCustom: false }
        ];
        
        setModels(allModels);
        setRoles(defaultRoles);
        
        console.log('Settings page loaded config:', config);
        console.log('Available models:', allModels);
        console.log('Available roles:', defaultRoles);
        
      } catch (error) {
        console.error('Failed to load configuration:', error);
      } finally {
        setLoading(false);
      }
    };

    loadConfig();

    // Listen for configuration changes
    const unsubscribe = configManager.onConfigChange((newConfig) => {
      console.log('Settings page detected config change:', newConfig);
      loadConfig();
    });

    // Cleanup listener on unmount
    return () => {
      unsubscribe();
    };
  }, []);

  const handleIndexingToggle = () => {
    setIndexingEnabled(!indexingEnabled);
    if (!indexingEnabled) {
      // Start indexing when enabled
      setIsIndexing(true);
      setIndexingProgress(0);
      // Simulate indexing progress
      const interval = setInterval(() => {
        setIndexingProgress(prev => {
          if (prev >= 100) {
            clearInterval(interval);
            setIsIndexing(false);
            return 100;
          }
          return prev + 10;
        });
      }, 500);
    }
  };

  return (
    <div className="h-screen w-full flex bg-vscode-bg-primary text-vscode-text-primary overflow-hidden">
      {/* Sidebar */}
      <div className="w-64 bg-vscode-bg-secondary border-r border-vscode-border flex flex-col">
        <div className="p-4 border-b border-vscode-border">
          <button
            onClick={onBack}
            className="text-vscode-text-secondary hover:text-vscode-text-primary text-sm flex items-center gap-2 transition-colors"
          >
            ← Back
          </button>
        </div>
        <nav className="flex-1 p-2">
          {navigationItems.map((item) => (
            <button
              key={item.id}
              onClick={() => setActiveTab(item.id)}
              className={`settings-sidebar-item w-full text-left px-3 py-2 rounded text-sm flex items-center gap-3 ${
                activeTab === item.id
                  ? 'bg-vscode-bg-tertiary text-vscode-text-primary active'
                  : 'text-vscode-text-secondary hover:text-vscode-text-primary'
              }`}
            >
              <span className="text-base">{item.icon}</span>
              {item.label}
            </button>
          ))}
        </nav>
      </div>

      {/* Main Content */}
      <div className="flex-1 flex flex-col overflow-hidden">
        <div className="p-6 border-b border-vscode-border">
          <h1 className="text-xl font-semibold text-vscode-text-primary">
            {navigationItems.find(item => item.id === activeTab)?.label || 'Settings'}
          </h1>
        </div>

        <div className="flex-1 overflow-y-auto p-6">
          {loading && (
            <div className="flex items-center justify-center h-full">
              <div className="text-vscode-text-secondary">Loading configuration...</div>
            </div>
          )}
          
          {!loading && activeTab === 'indexing' && (
            <div className="settings-content-section space-y-6 max-w-4xl">
              {/* Warning Alert */}
              <div className="settings-warning bg-yellow-900/20 border border-yellow-600/30 rounded-lg p-4">
                <div className="flex items-start gap-3">
                  <div className="flex-1">
                    
                    <div className="mt-4 space-y-3">
                      <div className="flex items-center justify-between">
                        <div>
                          <label className="text-vscode-text-primary font-medium">Enable indexing</label>
                          <p className="text-vscode-text-secondary text-xs mt-1">
                            Allows indexing of your codebase for search and context understanding.
                          </p>
                          <p className="text-vscode-text-secondary text-xs mt-1">
                            Note that indexing can consume significant system resources, especially on larger codebases.
                          </p>
                        </div>
                        <div className="relative">
                          <input
                            type="checkbox"
                            id="indexing-toggle"
                            checked={indexingEnabled}
                            onChange={handleIndexingToggle}
                            className="sr-only"
                          />
                          <button
                            onClick={handleIndexingToggle}
                            className={`settings-toggle relative inline-flex h-6 w-11 items-center rounded-full transition-colors ${
                              indexingEnabled ? 'bg-vscode-accent' : 'bg-vscode-bg-tertiary'
                            }`}
                          >
                            <span
                              className={`inline-block h-4 w-4 transform rounded-full bg-white transition-transform ${
                                indexingEnabled ? 'translate-x-6' : 'translate-x-1'
                              }`}
                            />
                          </button>
                        </div>
                      </div>
                    </div>
                  </div>
                </div>
              </div>

              {/* Codebase Index Section */}
              <div className="bg-vscode-bg-secondary rounded-lg p-4 border border-vscode-border">
                <h2 className="text-lg font-medium text-vscode-text-primary mb-3 flex items-center gap-2">
                  <span className="text-vscode-accent">@</span>
                  codebase index
                </h2>
                
                {indexingEnabled ? (
                  <div>
                    <div className="flex items-center justify-between mb-2">
                      <span className="text-vscode-text-secondary text-sm">
                        {isIndexing ? 'Initializing...' : 'Index complete'}
                      </span>
                      <span className="text-vscode-text-secondary text-xs">
                        {indexingProgress}%
                      </span>
                    </div>
                    
                    <div className="w-full bg-vscode-bg-tertiary rounded-full h-2">
                      <div
                        className="settings-progress-bar h-2 rounded-full transition-all duration-300"
                        style={{ width: `${indexingProgress}%` }}
                      />
                    </div>
                    
                    {isIndexing && (
                      <p className="text-vscode-text-secondary text-xs mt-2">
                        Indexing your codebase to improve AI understanding and responses...
                      </p>
                    )}
                  </div>
                ) : (
                  <p className="text-vscode-text-secondary text-sm">
                    Indexing is disabled. Enable it above to start indexing your codebase.
                  </p>
                )}
              </div>

              {/* Documentation Section */}
              <div className="bg-vscode-bg-secondary rounded-lg p-4 border border-vscode-border">
                <div className="flex items-center justify-between mb-3">
                  <h2 className="text-lg font-medium text-vscode-text-primary">Documentation</h2>
                  <button
                    onClick={handleAddDocumentation}
                    className="text-vscode-accent hover:text-vscode-accent-hover text-xl font-light transition-colors"
                    title="Add documentation source"
                  >
                    +
                  </button>
                </div>
                
                {documentationSources.length === 0 ? (
                  <div className="text-center py-8">
                    <div className="text-vscode-text-secondary text-sm mb-2">
                      No documentation sources configured.
                    </div>
                    <div className="text-vscode-text-secondary text-xs">
                      Click the <span className="text-vscode-accent">+</span> button to add your first docs.
                    </div>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {documentationSources.map((source) => (
                      <div
                        key={source.id}
                        className="flex items-center justify-between p-3 bg-vscode-bg-primary rounded border border-vscode-border"
                      >
                        <div className="flex items-center gap-3">
                          <span className="text-vscode-text-secondary">
                            {source.type === 'api' ? '◉' : source.type === 'docs' ? '◈' : '◇'}
                          </span>
                          <div>
                            <div className="text-vscode-text-primary text-sm font-medium">
                              {source.name}
                            </div>
                            <div className="text-vscode-text-secondary text-xs">
                              {source.url}
                            </div>
                          </div>
                        </div>
                        <button className="text-vscode-text-secondary hover:text-red-500 text-sm transition-colors">
                          Remove
                        </button>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {/* Models Tab */}
          {!loading && activeTab === 'models' && (
            <div className="settings-content-section space-y-6 max-w-4xl">
              <div className="bg-vscode-bg-secondary rounded-lg p-4 border border-vscode-border">
                <h2 className="text-lg font-medium text-vscode-text-primary mb-3 flex items-center gap-2">
                  <span className="text-vscode-accent">◆</span>
                  Available Models
                </h2>
                
                {models.length === 0 ? (
                  <div className="text-center py-8">
                    <div className="text-vscode-text-secondary text-sm mb-2">
                      No models configured.
                    </div>
                    <div className="text-vscode-text-secondary text-xs">
                      Models can be added through the chat interface.
                    </div>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {models.map((model, index) => (
                      <div
                        key={index}
                        className="flex items-center justify-between p-3 bg-vscode-bg-primary rounded border border-vscode-border"
                      >
                        <div className="flex items-center gap-3">
                          <span className="text-vscode-accent">◆</span>
                          <div>
                            <div className="text-vscode-text-primary text-sm font-medium">
                              {model.name || model.id || 'Unknown Model'}
                            </div>
                            <div className="text-vscode-text-secondary text-xs">
                              {model.provider || 'Custom'} • {model.contextLength ? `${model.contextLength} tokens` : 'Context unknown'}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          {model.isCustom && (
                            <>
                              <span className="text-vscode-text-secondary text-xs bg-vscode-bg-tertiary px-2 py-1 rounded">
                                Custom
                              </span>
                              <button 
                                onClick={() => handleRemoveModel(model.name)}
                                className="text-vscode-text-secondary hover:text-red-500 text-sm transition-colors"
                              >
                                Remove
                              </button>
                            </>
                          )}
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {/* Roles Tab */}
          {!loading && activeTab === 'roles' && (
            <div className="settings-content-section space-y-6 max-w-4xl">
              <div className="bg-vscode-bg-secondary rounded-lg p-4 border border-vscode-border">
                <h2 className="text-lg font-medium text-vscode-text-primary mb-3 flex items-center gap-2">
                  <span className="text-vscode-accent">≡</span>
                  Available Roles
                </h2>
                
                {roles.length === 0 ? (
                  <div className="text-center py-8">
                    <div className="text-vscode-text-secondary text-sm mb-2">
                      No roles configured.
                    </div>
                    <div className="text-vscode-text-secondary text-xs">
                      Roles can be added through the chat interface.
                    </div>
                  </div>
                ) : (
                  <div className="space-y-2">
                    {roles.map((role, index) => (
                      <div
                        key={index}
                        className="flex items-center justify-between p-3 bg-vscode-bg-primary rounded border border-vscode-border"
                      >
                        <div className="flex items-center gap-3">
                          <span className="text-vscode-accent">≡</span>
                          <div>
                            <div className="text-vscode-text-primary text-sm font-medium">
                              {role.name || role.id || 'Unknown Role'}
                            </div>
                            <div className="text-vscode-text-secondary text-xs">
                              {role.description || 'No description available'}
                            </div>
                          </div>
                        </div>
                        <div className="flex items-center gap-2">
                          {role.isCustom && (
                            <span className="text-vscode-text-secondary text-xs bg-vscode-bg-tertiary px-2 py-1 rounded">
                              Custom
                            </span>
                          )}
                          <button className="text-vscode-text-secondary hover:text-red-500 text-sm transition-colors">
                            Remove
                          </button>
                        </div>
                      </div>
                    ))}
                  </div>
                )}
              </div>
            </div>
          )}

          {/* Placeholder content for other tabs */}
          {!loading && activeTab !== 'indexing' && activeTab !== 'models' && activeTab !== 'roles' && (
            <div className="settings-content-section text-center py-12">
              <div className="text-vscode-text-secondary text-lg mb-2">
                {navigationItems.find(item => item.id === activeTab)?.icon}
              </div>
              <h3 className="text-vscode-text-primary font-medium mb-2">
                {navigationItems.find(item => item.id === activeTab)?.label} Settings
              </h3>
              <p className="text-vscode-text-secondary text-sm">
                Configuration options for {navigationItems.find(item => item.id === activeTab)?.label.toLowerCase()} will appear here.
              </p>
            </div>
          )}
        </div>
      </div>
    </div>
  );
};