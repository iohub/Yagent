import { LanguageParser, loadLanguageParsers } from './languageParser'

export class GlobalParser {
  private static instance: GlobalParser
  private parsers: LanguageParser | null = null
  private isInitializing = false
  private initPromise: Promise<void> | null = null

  private constructor() {}

  public static getInstance(): GlobalParser {
    if (!GlobalParser.instance) {
      GlobalParser.instance = new GlobalParser()
    }
    return GlobalParser.instance
  }

  public async initialize(): Promise<void> {
    if (this.isInitializing) {
      return this.initPromise!
    }
    if (this.parsers) {
      return
    }
    this.isInitializing = true
    this.initPromise = new Promise<void>(async (resolve, reject) => {
      try {
        // Pre-load the most common languages
        this.parsers = await loadLanguageParsers(new Set(['js', 'ts', 'py', 'java', 'cpp']))
        resolve()
      } catch (error) {
        reject(error)
      } finally {
        this.isInitializing = false
      }
    })
    return this.initPromise
  }

  public getParser(extension: string): { parser: import('web-tree-sitter').Parser; query: import('web-tree-sitter').Query } {
    if (!this.parsers) {
      throw new Error('Language parsers not initialized. Call initialize() first.')
    }
    const parser = this.parsers[extension]
    if (!parser) {
      throw new Error(`No parser available for extension: ${extension}`)
    }
    return parser
  }

  public async addLanguage(extension: string): Promise<void> {
    if (!this.parsers) {
      throw new Error('Language parsers not initialized. Call initialize() first.')
    }
    if (this.parsers[extension]) {
      return
    }
    const newParser = await loadLanguageParsers(new Set([extension]))
    this.parsers = { ...this.parsers, ...newParser }
  }

  public hasParser(extension: string): boolean {
    return this.parsers ? !!this.parsers[extension] : false
  }
}
