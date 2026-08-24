// The pre-commit hook runs `npx eslint <files>` from the repo root, but the
// frontend toolchain (eslint + plugins) lives in frontend/node_modules.
// Re-export the frontend config: Node resolves its bare imports
// (typescript-eslint, eslint-plugin-*) relative to frontend/eslint.config.js,
// so no root node_modules is needed. Flat-config file/ignore patterns are
// slash-less globs and match at any depth, so they still cover frontend/.
export { default } from './frontend/eslint.config.js'
