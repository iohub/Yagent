import React from 'react';
import { Bot } from 'lucide-react';

export const WelcomeMessage: React.FC = () => {
  return (
    <div className="flex-1 flex items-center justify-center p-6 bg-vscode-bg-primary">
      <div className="text-center max-w-sm">
        <div className="mb-4">
          <Bot className="w-8 h-8 text-vscode-text-secondary mx-auto mb-2" />
        </div>
        
        <h2 className="text-lg font-medium text-vscode-text-primary mb-2">
          CodeActor AI
        </h2>
        
        <p className="text-vscode-text-secondary text-xs">
          How can I help you today?
        </p>
      </div>
    </div>
  );
};