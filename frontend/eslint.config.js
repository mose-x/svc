import js from '@eslint/js'
import globals from 'globals'
import reactHooks from 'eslint-plugin-react-hooks'
import reactRefresh from 'eslint-plugin-react-refresh'
import tseslint from 'typescript-eslint'

// Flat config (eslint 9). Mirrors the standard Vite react-ts template so the
// pre-commit hook actually lints instead of skipping every file.
export default tseslint.config(
  { ignores: ['dist', 'wailsjs'] },
  {
    extends: [js.configs.recommended, ...tseslint.configs.recommended],
    files: ['**/*.{ts,tsx}'],
    languageOptions: {
      ecmaVersion: 2020,
      globals: globals.browser,
    },
    plugins: {
      'react-hooks': reactHooks,
      'react-refresh': reactRefresh,
    },
    rules: {
      ...reactHooks.configs.recommended.rules,
      'react-refresh/only-export-components': [
        'warn',
        { allowConstantExport: true },
      ],
      // Wails bindings return `any`; the codebase relies on explicit `any`
      // casts for error payloads and settings patches.
      '@typescript-eslint/no-explicit-any': 'off',
    },
  },
)
