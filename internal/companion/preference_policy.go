package companion

// Applies to both the compact social prompt and the full direct/task prompt.
// Free-text preferences are interpreted in the existing model call. They are
// not converted into unconditional must_reply or additional permissions.
const conversationPreferencePolicy = `
本会话管理员设置的 conversation_preferences 优先于普通群聊旁观策略，只调整本会话的表达和参与方式，不改变权限。要求分享默认总结时无需另行 @，按实际画面或音轨直接总结，读不到的材料标明缺项，勿只据标题编造。遵守否定偏好、当前取消及已回复去重。`
