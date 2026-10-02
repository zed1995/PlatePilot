import { theme as antdTheme } from 'antd'
import type { ThemeConfig } from 'antd'

// Apple-flavoured console theme: near-black text on white cards over a light
// grey canvas, hairline borders, one restrained accent, generous radii. All
// values live here so pages never hardcode colours.
export const consoleTheme: ThemeConfig = {
  algorithm: antdTheme.defaultAlgorithm,
  token: {
    // Monochrome accent: the primary colour is the near-black itself, so
    // buttons, links, switches, and focus rings all stay grey-scale. Colour is
    // reserved for semantics only (error red, warning orange).
    colorPrimary: '#1d1d1f',
    colorInfo: '#1d1d1f',
    colorLink: '#1d1d1f',
    colorSuccess: '#34c759',
    colorWarning: '#ff9500',
    colorError: '#ff3b30',
    colorText: '#1d1d1f',
    colorTextSecondary: '#6e6e73',
    colorTextTertiary: '#86868b',
    colorTextQuaternary: '#aeaeb2',
    colorBgLayout: '#f5f5f7',
    colorBgContainer: '#ffffff',
    colorBorder: 'rgba(0, 0, 0, 0.10)',
    colorBorderSecondary: 'rgba(0, 0, 0, 0.06)',
    borderRadius: 8,
    borderRadiusLG: 12,
    fontSize: 14,
    fontFamily:
      "-apple-system, BlinkMacSystemFont, 'SF Pro Text', 'Helvetica Neue', 'PingFang SC', 'Segoe UI', Roboto, sans-serif",
    boxShadow: '0 1px 2px rgba(0, 0, 0, 0.04), 0 2px 12px rgba(0, 0, 0, 0.03)',
    boxShadowSecondary: '0 4px 20px rgba(0, 0, 0, 0.08)',
  },
  components: {
    Layout: {
      bodyBg: '#f5f5f7',
      siderBg: 'transparent',
      headerBg: 'transparent',
    },
    Menu: {
      itemBg: 'transparent',
      itemColor: '#424245',
      itemHoverColor: '#1d1d1f',
      itemSelectedColor: '#1d1d1f',
      itemSelectedBg: 'rgba(0, 0, 0, 0.06)',
      itemBorderRadius: 8,
      itemMarginInline: 10,
      itemMarginBlock: 2,
      activeBarBorderWidth: 0,
    },
    Table: {
      headerBg: 'transparent',
      headerColor: '#6e6e73',
      headerSplitColor: 'transparent',
      borderColor: 'rgba(0, 0, 0, 0.06)',
      rowHoverBg: 'rgba(0, 0, 0, 0.02)',
      cellPaddingBlock: 12,
      cellPaddingInline: 14,
    },
    Button: {
      primaryShadow: 'none',
      defaultShadow: 'none',
      dangerShadow: 'none',
    },
    Tag: {
      defaultBg: 'rgba(0, 0, 0, 0.04)',
    },
    Statistic: {
      titleFontSize: 13,
      contentFontSize: 26,
    },
    Progress: {
      remainingColor: 'rgba(0, 0, 0, 0.06)',
    },
  },
}
