// Correctness only, no style.
//
// This frontend is about six thousand lines of hand-written JavaScript with no
// static checking at all, and the faults it has produced were the plain kind:
// a symbol imported but never re-exported, which turned CI red and which the
// unit tests could not see because they do not build the app. A linter sees
// that before a user does.
//
// Formatting rules are deliberately absent. A style pass over a file this size
// would bury the findings that matter in thousands of lines of noise, and the
// point of this config is that a report means something.

import js from '@eslint/js';
import globals from 'globals';

export default [
  {
    ignores: ['dist/**', 'wailsjs/**', 'node_modules/**'],
  },
  js.configs.recommended,
  {
    files: ['**/*.js', '**/*.mjs'],
    languageOptions: {
      ecmaVersion: 2023,
      sourceType: 'module',
      globals: {
        ...globals.browser,
        ...globals.es2021,
      },
    },
    rules: {
      // The ones that catch real faults.
      'no-undef': 'error',
      // A warning, not an error. There are 59 of these in the existing code,
      // all dead bindings rather than faults, and blocking CI on them would
      // mean either a large mechanical cleanup at the wrong moment or turning
      // the rule off and losing it. Visible in the log, does not stop a PR.
      'no-unused-vars': ['warn', {
        // An unused function ARGUMENT is often documentation of a callback's
        // shape, so only complain about the ones after the last used one.
        args: 'after-used',
        // A caught error that is deliberately ignored is idiomatic here: the
        // codebase is full of best-effort paths that must never throw.
        caughtErrors: 'none',
        varsIgnorePattern: '^_',
        argsIgnorePattern: '^_',
      }],
      'no-const-assign': 'error',
      'no-dupe-keys': 'error',
      'no-dupe-args': 'error',
      'no-duplicate-case': 'error',
      'no-unreachable': 'error',
      'no-self-compare': 'error',
      'no-template-curly-in-string': 'error',
      'require-atomic-updates': 'off',
      // A value assigned and then replaced before it is read is a smell, not a
      // fault, and this codebase has a few by design. Off, so the report stays
      // worth reading.
      'no-useless-assignment': 'off',

      // Style, off on purpose.
      'no-empty': 'off',
      'no-prototype-builtins': 'off',
    },
  },
  {
    // Tests run under vitest, which brings its own globals.
    files: ['**/*.test.js'],
    languageOptions: {
      globals: {
        ...globals.browser,
        ...globals.node, // tests run under vitest in Node, so Buffer and friends exist
        describe: 'readonly', it: 'readonly', expect: 'readonly',
        beforeEach: 'readonly', afterEach: 'readonly', vi: 'readonly',
      },
    },
  },
  {
    // Build scripts run in Node.
    files: ['scripts/**/*.mjs', '*.config.js'],
    languageOptions: { globals: { ...globals.node } },
  },
];
