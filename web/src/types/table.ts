/**
 * 表格行数据的基础约束
 *
 * 为什么单独成文件：Vue 的 `generic="T extends TableRowData"` 属性
 * 由编译器在模块层面处理，**看不到** `<script setup>` 内部声明的类型，
 * 因此这类被泛型参数引用的类型必须能被 import 进来。
 *
 * 与 naive-ui 内部 RowData（`Record<string, any>`）等价，
 * 但用 unknown 而不是 any：从行数据读字段时仍有类型检查，
 * 需要具体类型时由列定义的泛型参数（如 `DataTableColumns<ImageInfo>`）还原。
 */
export type TableRowData = Record<string, unknown>
