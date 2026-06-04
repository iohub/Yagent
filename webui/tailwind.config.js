/** @type {import('tailwindcss').Config} */
module.exports = {
  content: [
    "./src/**/*.{js,jsx,ts,tsx}",
  ],
  theme: {
    extend: {
      colors: {
        vscode: {
          bg: {
            primary: 'var(--vscode-editor-background)',
            secondary: 'var(--vscode-sideBar-background)',
            tertiary: 'var(--vscode-editorWidget-background)',
          },
          text: {
            primary: 'var(--vscode-editor-foreground)',
            secondary: 'var(--vscode-descriptionForeground)',
          },
          border: 'var(--vscode-widget-border)',
          accent: 'var(--vscode-button-background)',
          'accent-hover': 'var(--vscode-button-hoverBackground)',
        }
      },
      fontFamily: {
        sans: ['var(--vscode-font-family)', 'ui-sans-serif', 'system-ui'],
        mono: ['var(--vscode-editor-font-family)', 'ui-monospace', 'monospace'],
      },
      fontSize: {
        xs: ['var(--vscode-font-size)', '1.4'],
        sm: ['var(--vscode-font-size)', '1.5'],
        base: ['var(--vscode-font-size)', '1.6'],
      }
    },
  },
  plugins: [],
}