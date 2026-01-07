import { useState } from 'react';
import { Button } from './ui/Button';
import { Input } from './ui/Input';
import { Play } from 'lucide-react';

interface TaskFormProps {
  onSubmit: (projectDir: string, taskDesc: string) => void;
  isLoading?: boolean;
}

export function TaskForm({ onSubmit, isLoading }: TaskFormProps) {
  const [projectDir, setProjectDir] = useState('');
  const [taskDesc, setTaskDesc] = useState('');

  const handleSubmit = (e: React.FormEvent) => {
    e.preventDefault();
    if (projectDir.trim() && taskDesc.trim()) {
      onSubmit(projectDir, taskDesc);
    }
  };

  return (
    <form onSubmit={handleSubmit} className="space-y-3">
      <div>
        <label htmlFor="projectDir" className="block text-xs font-medium text-[#969696] mb-1">
          Project Directory
        </label>
        <Input
          id="projectDir"
          value={projectDir}
          onChange={(e) => setProjectDir(e.target.value)}
          placeholder="/path/to/project"
          required
        />
      </div>
      <div>
        <label htmlFor="taskDesc" className="block text-xs font-medium text-[#969696] mb-1">
          Task Description
        </label>
        <Input
          id="taskDesc"
          value={taskDesc}
          onChange={(e) => setTaskDesc(e.target.value)}
          placeholder="Describe what needs to be done..."
          required
        />
      </div>
      <Button type="submit" disabled={isLoading} className="w-full">
        {isLoading ? 'Starting...' : (
          <>
            <Play className="mr-2 h-3 w-3" /> Start Task
          </>
        )}
      </Button>
    </form>
  );
}
