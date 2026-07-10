// vitest.config.js — test-runner config only. Deliberately NOT vite.config.js:
// `vite build` finds no config file and keeps its current defaults, so adding
// tests cannot change the production bundle. Tests cover the pure logic
// modules (groups.js, utils.js); no DOM environment on purpose — modules must
// stay import-safe without a document (the view-extraction trap).
import { defineConfig } from 'vitest/config';

export default defineConfig({
  test: {
    environment: 'node',
    include: ['src/**/*.test.js'],
  },
});
