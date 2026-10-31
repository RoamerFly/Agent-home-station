# 管理工作台视觉与动效

## 已选方向

以紧凑的工作台承载聊天、任务和插件操作，使用局部科技光效。保留顶栏保存、左下主题切换、已有表单草稿及全部业务入口。

- 浅色：冷白 `#f7f9fc`、面板白 `#ffffff`、石墨 `#202c3d`、次级灰 `#5d6d82`、蓝 `#225cce`。
- 深色：墨蓝 `#0b1018`、面板 `#121b28`、浮层 `#192536`、文字 `#e7edf7`、冰蓝 `#8bbcff`。
- 字体：本机 Segoe UI、微软雅黑；正文 14px，页面标题 18–19px，数字使用等宽数字。
- 布局：侧栏是固定导航轨，顶栏容纳当前位置、账号和保存，内容占满其余区域；列表与详情共用连续工作区，不添加装饰性空卡片。
- 动效：点击立即响应；导航标记可中途转向；详情短距离进入；不逐张卡片延迟出现，不因状态轮询重播动画。
- 光效：选中边框与登录/启动的一次性光幕，不添加持续粒子、鼠标追踪光源或全屏模糊。

## 来源与应用边界

项目内安装 `web-animation-design`、`fixing-motion-performance`、`aceternity-ui`。前两项约束动效节奏和成本，第三项用作视觉参考。主体为原生 JS/CSS，本轮不引入 React、Tailwind 或组件运行时，也不复制付费模板。

参考：
- https://github.com/vercel-labs/open-agents/tree/main/.agents/skills/web-animation-design
- https://github.com/ibelick/ui-skills/tree/main/skills/fixing-motion-performance
- https://github.com/TerminalSkills/skills/tree/main/skills/aceternity-ui

## 验收

使用本地合成数据和拦截写入的浏览器检查，覆盖 19 页、浅深色、窄窗/桌面；检查保存位置、草稿保留、导航快速切换、减少动态效果、横向溢出。截图保留于忽略目录。浏览器验收不等同于桌面程序实机帧率测量或正式安装。
