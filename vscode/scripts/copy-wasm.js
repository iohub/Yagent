/**
 * Copies the required tree-sitter WASM files from node_modules/tree-sitter-wasms/out/
 * into the compiled output directory (out/) so that the extension can load them at runtime.
 */
const fs = require('fs')
const path = require('path')

const wasmSource = path.join(__dirname, '..', 'node_modules', 'tree-sitter-wasms', 'out')
const wasmDest = path.join(__dirname, '..', 'out')

const wasmFiles = [
  'tree-sitter-javascript.wasm',
  'tree-sitter-typescript.wasm',
  'tree-sitter-tsx.wasm',
  'tree-sitter-python.wasm',
  'tree-sitter-rust.wasm',
  'tree-sitter-go.wasm',
  'tree-sitter-cpp.wasm',
  'tree-sitter-c.wasm',
  'tree-sitter-c_sharp.wasm',
  'tree-sitter-ruby.wasm',
  'tree-sitter-java.wasm',
  'tree-sitter-php.wasm',
  'tree-sitter-swift.wasm',
]

// web-tree-sitter ships tree-sitter.wasm which must be present at __dirname
const webTreeSitterWasm = path.join(
  __dirname, '..', 'node_modules', 'web-tree-sitter', 'tree-sitter.wasm'
)

if (!fs.existsSync(wasmDest)) {
  fs.mkdirSync(wasmDest, { recursive: true })
}

let copied = 0

for (const file of wasmFiles) {
  const src = path.join(wasmSource, file)
  const dst = path.join(wasmDest, file)
  if (fs.existsSync(src)) {
    fs.copyFileSync(src, dst)
    console.log(`Copied ${file}`)
    copied++
  } else {
    console.warn(`Warning: ${file} not found in ${wasmSource}`)
  }
}

if (fs.existsSync(webTreeSitterWasm)) {
  const dst = path.join(wasmDest, 'tree-sitter.wasm')
  fs.copyFileSync(webTreeSitterWasm, dst)
  console.log('Copied tree-sitter.wasm')
  copied++
} else {
  console.warn('Warning: web-tree-sitter/tree-sitter.wasm not found')
}

console.log(`\nDone — copied ${copied} WASM file(s) to ${wasmDest}`)
