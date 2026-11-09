// Arkade 共享协议模块 arkade/protocol。
//
// 该 module 被 hub 与 agent 通过 replace 引用，保证消息信封、方法名与状态模型
// 只有一份定义，避免两端各写一套导致协议漂移。
module arkade/protocol

go 1.24
