// ESLint flat config for the console's static JavaScript
// (internal/console/static). Run through tests/console/lint.sh.
//
// The console is plain browser script (no modules, no bundler): each file
// is a classic <script> that reads and writes a handful of shared globals.
// Those are declared here so that every other free identifier is an error
// (no-undef) — the class of bug this catches is a helper missing from a
// view's `const { ... } = ui` destructuring, which only fails at runtime
// when that code path runs in a browser.
'use strict';

const globals = require('globals');

module.exports = [
  {
    files: ['**/*.js'],
    languageOptions: {
      ecmaVersion: 2022,
      sourceType: 'script',
      globals: {
        ...globals.browser,
        // Console globals. Each file assigns its own object onto window
        // (window.ui = ..., window.app = ...) and the others read it;
        // `views` is the only one extended by several files.
        ui: 'readonly',
        api: 'readonly',
        app: 'readonly',
        settings: 'readonly',
        views: 'writable',
      },
    },
    rules: {
      'no-undef': 'error',
      // `args: none`: callbacks routinely take (e) or (row, i) and use only
      // some. `caughtErrors: none`: the code base writes `catch (e) { ... }`
      // and ignores e when the failure is expected (clipboard, JSON parse).
      'no-unused-vars': ['error', { vars: 'all', args: 'none', caughtErrors: 'none' }],
      'no-redeclare': 'error',
      'no-shadow-restricted-names': 'error',
      // Top-level let/const/class in a classic script land in the global
      // lexical scope shared by every <script>; two files declaring the
      // same name is a load-time SyntaxError that breaks the whole app.
      // Files wrap themselves in an IIFE instead.
      'no-implicit-globals': ['error', { lexicalBindings: true }],
      eqeqeq: 'warn',
    },
  },
];
