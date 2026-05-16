/** @type {import('tailwindcss').Config} */
export default {
  content: ['./src/**/*.{astro,html,js,jsx,md,mdx,svelte,ts,tsx,vue}'],
  theme: {
    extend: {
      colors: {
        brand: {
          50: '#f4f1ec',
          100: '#e3dccd',
          300: '#b9aa8a',
          500: '#8a724a',
          700: '#5f4e2f',
          900: '#332916',
        },
        accent: {
          500: '#d65a31',
          600: '#bf4a25',
        },
      },
      fontFamily: {
        sans: ['"Inter"', '"Helvetica Neue"', 'system-ui', 'sans-serif'],
        mono: ['"JetBrains Mono"', '"Source Code Pro"', 'ui-monospace', 'monospace'],
      },
      maxWidth: {
        prose: '68ch',
      },
    },
  },
  plugins: [],
};
