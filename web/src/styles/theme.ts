import type { GlobalThemeOverrides } from 'naive-ui'

/**
 * Naive UI 主题覆盖
 *
 * 目的：统一主色与圆角，让表格/按钮/徽标观感一致，而不是各处默认值拼凑。
 * 只覆盖必要 token，保持 Naive 默认的间距与交互细节。
 */
export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#2080f0',
    primaryColorHover: '#4098fc',
    primaryColorPressed: '#1060c9',
    primaryColorSuppl: '#4098fc',
    borderRadius: '6px',
    fontSize: '14px'
  },
  Table: {
    // 离线行整行置灰时，表头也要与内容协调
    thColor: '#fafafc',
    thFontWeight: '500'
  }
}
