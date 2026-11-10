/**
 * 拉镜像输出纯函数（兼容层）
 *
 * 实现已泛化到 `utils/stackOperationOutput.ts`——输出处理与具体动作无关，
 * 启动/重建/删除的规则完全一样，没必要维护两份。
 *
 * 这里只做 re-export，保持旧引用（`useStackPullTask` 等）可用。
 * **不要在这里加逻辑**：加了两份实现迟早分叉，一处修了另一处没修。
 */
export {
  MAX_OUTPUT_BYTES,
  MAX_OUTPUT_EVENTS,
  byteLength,
  mergeOutput,
  stripAnsi,
  truncateTailToBytes,
  segmentOutput,
  type OutputChunk,
  type OutputEvent,
  type OutputSegment
} from './stackOperationOutput'
