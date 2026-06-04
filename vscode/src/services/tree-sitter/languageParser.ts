import * as path from 'path'
import Parser from 'web-tree-sitter'
import {
  javascriptQuery,
  typescriptQuery,
  pythonQuery,
  rustQuery,
  goQuery,
  cppQuery,
  cQuery,
  csharpQuery,
  rubyQuery,
  javaQuery,
  phpQuery,
  swiftQuery,
} from './queries'

export type LanguageParser = {
  [ext: string]: { parser: Parser; query: Parser.Query }
}

async function loadLanguage(langName: string): Promise<Parser.Language> {
  await initializeParser()
  return await Parser.Language.load(path.join(__dirname, `tree-sitter-${langName}.wasm`))
}

let isParserInitialized = false

async function initializeParser(): Promise<void> {
  if (!isParserInitialized) {
    await Parser.init({
      // Tell Emscripten where to find the WASM binary.
      // In the compiled extension the file is copied next to extension.js.
      locateFile: (scriptName: string) => path.join(__dirname, scriptName),
    })
    isParserInitialized = true
  }
}

export async function loadRequiredLanguageParsers(filesToParse: string[]): Promise<LanguageParser> {
  const extensionsToLoad = new Set(filesToParse.map((file) => path.extname(file).toLowerCase().slice(1)))
  return await loadLanguageParsers(extensionsToLoad)
}

export async function loadLanguageParsers(extensionsToLoad: Set<string>): Promise<LanguageParser> {
  await initializeParser()
  const parsers: LanguageParser = {}
  for (const ext of extensionsToLoad) {
    let language: Parser.Language
    let query: Parser.Query
    switch (ext) {
      case 'js':
      case 'jsx':
        language = await loadLanguage('javascript')
        query = language.query(javascriptQuery)
        break
      case 'ts':
        language = await loadLanguage('typescript')
        query = language.query(typescriptQuery)
        break
      case 'tsx':
        language = await loadLanguage('tsx')
        query = language.query(typescriptQuery)
        break
      case 'py':
        language = await loadLanguage('python')
        query = language.query(pythonQuery)
        break
      case 'rs':
        language = await loadLanguage('rust')
        query = language.query(rustQuery)
        break
      case 'go':
        language = await loadLanguage('go')
        query = language.query(goQuery)
        break
      case 'cxx':
      case 'cpp':
      case 'hpp':
        language = await loadLanguage('cpp')
        query = language.query(cppQuery)
        break
      case 'c':
      case 'h':
        language = await loadLanguage('c')
        query = language.query(cQuery)
        break
      case 'cs':
        language = await loadLanguage('c_sharp')
        query = language.query(csharpQuery)
        break
      case 'rb':
        language = await loadLanguage('ruby')
        query = language.query(rubyQuery)
        break
      case 'java':
        language = await loadLanguage('java')
        query = language.query(javaQuery)
        break
      case 'php':
        language = await loadLanguage('php')
        query = language.query(phpQuery)
        break
      case 'swift':
        language = await loadLanguage('swift')
        query = language.query(swiftQuery)
        break
      default:
        continue
    }
    const parser = new Parser()
    parser.setLanguage(language)
    parsers[ext] = { parser, query }
  }
  return parsers
}