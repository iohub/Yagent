import { Moon, Sun, Laptop } from "lucide-react"
import { Button } from "./ui/Button"
import { useTheme } from "./theme-provider"

export function ThemeToggle() {
  const { theme, setTheme } = useTheme()

  return (
    <div className="flex items-center gap-1 border border-border rounded-md p-0.5 bg-secondary/50">
      <Button
        variant="ghost"
        size="sm"
        className={`h-6 w-6 px-0 ${theme === 'light' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground'}`}
        onClick={() => setTheme("light")}
        title="Light"
      >
        <Sun className="h-4 w-4" />
        <span className="sr-only">Light</span>
      </Button>
      <Button
        variant="ghost"
        size="sm"
        className={`h-6 w-6 px-0 ${theme === 'dark' ? 'bg-background text-foreground shadow-sm' : 'text-muted-foreground'}`}
        onClick={() => setTheme("dark")}
        title="Dark"
      >
        <Moon className="h-4 w-4" />
        <span className="sr-only">Dark</span>
      </Button>
    </div>
  )
}
