import type { GlobalThemeOverrides } from 'naive-ui'

/**
 * Naive UI 主题覆盖（浅色 / 深色两套）
 *
 * 目的：统一主色与圆角，让表格/按钮/徽标观感一致，而不是各处默认值拼凑。
 *
 * ## 为什么是两套而不是一套
 * Naive 的主题覆盖值是**具体色值**（会被写进内联样式），无法用 CSS 变量跟着
 * data-theme 自动切换。所以深色必须再声明一份，色值与本项目
 * styles/global.css 里的深色变量保持同源——改色时两处一起改。
 *
 * ## 行高
 * 表格统一走 `size="medium"`，这里把 medium 的内边距从 12px 抬到 14px，
 * 单行高度约 50px：运维表要扫几十行，行高太密会看错行；
 * 太松又浪费屏幕，14px 是「一眼能对齐」和「一屏看二十行」的折中。
 */
const SPACING = {
  // 表头与单元格的垂直内边距：medium 档统一加高
  thPadding: '14px 14px',
  tdPadding: '14px 14px'
} as const

/** 浅色主题覆盖 */
export const themeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#2080f0',
    primaryColorHover: '#4098fc',
    primaryColorPressed: '#1060c9',
    primaryColorSuppl: '#4098fc',
    borderRadius: '6px',
    fontSize: '14px',
    bodyColor: '#f5f7fa'
  },
  DataTable: {
    // 离线行整行置灰时，表头也要与内容协调
    thColor: '#fafbfd',
    thFontWeight: '500',
    thPaddingMedium: SPACING.thPadding,
    tdPaddingMedium: SPACING.tdPadding
  }
}

/**
 * 深色主题覆盖。
 *
 * 主色提亮到 #4a9eff：深底上的原主色 #2080f0 对比度不够，链接会发暗。
 * 表头用比内容更深的底色而不是更浅——深色界面里"往后退"才像表头。
 */
export const darkThemeOverrides: GlobalThemeOverrides = {
  common: {
    primaryColor: '#4a9eff',
    primaryColorHover: '#6bb0ff',
    primaryColorPressed: '#2f80e0',
    primaryColorSuppl: '#6bb0ff',
    borderRadius: '6px',
    fontSize: '14px',
    bodyColor: '#14161a'
  },
  DataTable: {
    thColor: '#1a1d24',
    thFontWeight: '500',
    thPaddingMedium: SPACING.thPadding,
    tdPaddingMedium: SPACING.tdPadding
  }
}
