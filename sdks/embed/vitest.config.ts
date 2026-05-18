import { defineConfig } from 'vitest/config';

// happy-dom gives us a DOM that supports Custom Elements +
// Shadow DOM + postMessage out of the box — much faster than
// jsdom and matches the real-browser surface we care about.
export default defineConfig({
  test: {
    environment: 'happy-dom',
    globals: false,
  },
});
