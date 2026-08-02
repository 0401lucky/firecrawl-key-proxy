/** @type {import('tailwindcss').Config} */
export default {
  darkMode: 'class',
  content: ['./index.html', './src/**/*.{vue,ts}'],
  theme: {
    extend: {
      colors: {
        // 仪表台配色：深蓝黑基底 + 琥珀高亮 + 青色数据
        ink: {
          DEFAULT: '#0a0e17',   // 页面底色
          raised: '#10162a',    // 面板
          line: '#1c2540',      // 分隔线
        },
        lamp: {
          amber: '#f5a623',     // 高亮/冷却
          cyan: '#38c6d9',      // 数据/链接
          ok: '#34d399',        // 可用
          dead: '#64748b',      // 耗尽/停用
          bad: '#f87171',       // 失效
        },
      },
      fontFamily: {
        sans: [
          '"PingFang SC"', '"Microsoft YaHei"', '"Noto Sans SC"',
          'system-ui', '-apple-system', 'sans-serif',
        ],
        mono: [
          '"Cascadia Code"', '"JetBrains Mono"', 'Consolas',
          '"SFMono-Regular"', 'monospace',
        ],
      },
      boxShadow: {
        panel: '0 1px 0 rgba(255,255,255,0.03) inset, 0 8px 24px rgba(0,0,0,0.35)',
        lamp: '0 0 0 1px rgba(245,166,35,0.35), 0 0 18px rgba(245,166,35,0.18)',
      },
      keyframes: {
        rise: {
          '0%': { opacity: '0', transform: 'translateY(6px)' },
          '100%': { opacity: '1', transform: 'translateY(0)' },
        },
        toastIn: {
          '0%': { opacity: '0', transform: 'translateY(-8px) scale(0.98)' },
          '100%': { opacity: '1', transform: 'translateY(0) scale(1)' },
        },
      },
      animation: {
        rise: 'rise 0.35s ease-out both',
        toastIn: 'toastIn 0.2s ease-out both',
      },
    },
  },
  plugins: [],
}
