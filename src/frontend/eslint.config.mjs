import js from '@eslint/js'
import react from 'eslint-plugin-react'
import reactHooks from 'eslint-plugin-react-hooks'
import globals from 'globals'

// Lean, targeted lint gate. Goal: fail the build on the bug CLASS that slips past
// `vite build` — undefined identifiers/components, hook misuse, duplicate keys — while
// keeping stylistic findings as non-blocking warnings so the existing code isn't
// flooded. `npm run build` runs `eslint .` first, so these fail fast (incl. the
// Docker image build). (.mjs so Node loads it as ESM regardless of package.json type.)
export default [
  { ignores: ['dist/**', 'node_modules/**', '**/*.config.js'] },
  {
    files: ['**/*.{js,jsx}'],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: 'module',
      globals: { ...globals.browser, ...globals.es2021 },
      parserOptions: { ecmaFeatures: { jsx: true } },
    },
    settings: { react: { version: 'detect' } },
    plugins: { react, 'react-hooks': reactHooks },
    rules: {
      // ── Errors: the high-value bug class (these BREAK the build) ──
      'no-undef': 'error',                 // catches `depsApplies is not defined`
      'react/jsx-no-undef': 'error',       // catches an undefined JSX component
      'no-dupe-keys': 'error',
      'no-dupe-args': 'error',
      'no-const-assign': 'error',
      'no-obj-calls': 'error',
      'no-unreachable': 'error',
      'react-hooks/rules-of-hooks': 'error',
      // ── Warnings: informative, do NOT block (no --max-warnings) ──
      'no-unused-vars': ['warn', { argsIgnorePattern: '^_', varsIgnorePattern: '^_' }],
      'react-hooks/exhaustive-deps': 'warn',
      'no-empty': ['warn', { allowEmptyCatch: true }],
    },
  },
]
