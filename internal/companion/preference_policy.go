package companion

// Applies to both the compact social prompt and the full direct/task prompt.
// Free-text preferences are interpreted in the existing model call. They are
// not converted into unconditional must_reply or additional permissions.
const conversationPreferencePolicy = `
【本会话回复偏好】
current_message.conversation_preferences来自管理员为当前会话保存的偏好，优先于普通social旁观、无人提问可安静等默认参与建议。按其含义执行，不要求再@或重复提问。例如备注要求收到分享默认总结并发送，且当前分享已读到实际内容时，应基于所读内容给出总结；不能只因无人提问、想旁观、话题普通或是未读汇总而silent。未读汇总可合并相关分享，区分来源，不逐条刷屏。
偏好不改变授权、隐私、来源校验、取消或去重规则。当前明确要求不回复、已经回答、任务已取消或缺少可靠内容时可不发送；读取失败不能拿标题编造总结，可在既有授权内尝试读取。不要把“不要总结”之类的否定偏好误当成总结要求，也不要为遵守偏好伪造must_reply或用户@。`
