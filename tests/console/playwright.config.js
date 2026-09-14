// Playwright Test configuration for the console smoke test. Run through
// tests/console/run.sh, which sets BASE_URL, PW_RESULTS and CONSOLE_BROWSERS.
'use strict';

const path = require('path');
const { defineConfig, devices } = require('@playwright/test');

const results = process.env.PW_RESULTS || path.join(__dirname, 'results');
const browsers = (process.env.CONSOLE_BROWSERS || 'chromium').split(',').map((s) => s.trim()).filter(Boolean);
const all = { chromium: devices['Desktop Chrome'], firefox: devices['Desktop Firefox'], webkit: devices['Desktop Safari'] };
const names = browsers.includes('all') ? Object.keys(all) : browsers;
for (const n of names) if (!all[n]) throw new Error('unknown browser in CONSOLE_BROWSERS: ' + n);

module.exports = defineConfig({
  testDir: __dirname,
  testMatch: /.*\.spec\.js$/,
  fullyParallel: true,
  workers: Number(process.env.CONSOLE_WORKERS || 4),
  retries: 0,
  timeout: 60_000,
  expect: { timeout: 10_000 },
  outputDir: path.join(results, 'artifacts'),
  reporter: [['list'], ['junit', { outputFile: path.join(results, 'junit.xml') }], ['json', { outputFile: path.join(results, 'report.json') }]],
  use: {
    baseURL: process.env.BASE_URL || 'http://127.0.0.1:9000',
    actionTimeout: 15_000,
    screenshot: 'only-on-failure',
    video: 'retain-on-failure',
    trace: 'retain-on-failure',
    // The container is throwaway and runs as an unprivileged user without
    // user namespaces; Chromium's own sandbox cannot start there.
    chromiumSandbox: false,
  },
  projects: names.map((name) => ({ name, use: { ...all[name] } })),
});
