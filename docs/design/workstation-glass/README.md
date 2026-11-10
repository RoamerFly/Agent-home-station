# 大肥鱼的家 · 夜海玻璃界面设计

这组 Windows 桌面设计图覆盖聊天、二维记忆宫殿和统一应用中心。应用中心的两张图展示同一布局从微信切换到抖音。所有聊天、人物和应用状态均为合成示例。

| 视图 | 设计图 | 交互重点 |
| --- | --- | --- |
| 聊天 | [01-chat.png](01-chat.png) | 聊天和输入区为主，角色与近期记忆放在辅助列 |
| 二维记忆宫殿 | [02-memory-palace.png](02-memory-palace.png) | 按会话和类别浏览，关系图与列表共享选择，详情追溯原话、来源及更正 |
| 应用中心 · 微信选中 | [03-app-center-wechat.png](03-app-center-wechat.png) | 用统一应用卡片进入详情，查看微信的历史、记忆接续及发送能力 |
| 应用中心 · 抖音选中 | [04-app-center-douyin.png](04-app-center-douyin.png) | 在同一详情结构中查看抖音的浏览、发布及直播能力 |

## 共用视觉

- 深海底色 `#0B1832`，墨蓝卡片 `#18335A`，交互底色 `#2C5680`。浅青 `#91DCE7` 标示选中，浅紫 `#C8BEFF` 承载分类及待核对状态，主要文字用 `#F1F7FF`。
- 夜海插画和潮汐等高线留在背景、角色卡及关系图。正文、列表和操作区用较深且更不透明的磨砂玻璃。以细边框、少量高光和稳定的间距区分容器。
- 中文采用系统无衬线字体，正文、状态和按钮统一左对齐。画稿的标题和角色比例在实现时按同一标尺收敛。窄窗口收起角色辅助列，应用详情切为单独面板，记忆宫殿可切换为列表。高对比和减少视觉效果模式下使用实色卡片。

## 统一应用结构

| 区域 | 共用内容 | 微信示例 | 抖音示例 |
| --- | --- | --- | --- |
| 应用卡片 | 图标、名称、运行状态、账号状态、授权摘要和管理入口 | 原生服务已运行，原账号待登录 | 网页通道就绪，当前账号仅获准浏览 |
| 概览 | 当前可执行的动作、阻塞原因、最近结果 | 提示重新认证，展示已有连接 | 提示网页登录过期或能力状态 |
| 账号与权限 | 连接列表、登录状态、动作授权及撤销 | 每个账号与会话独立授权 | 浏览、互动、发布及直播分别授权 |
| 应用能力 | 能力名称、状态、原因及可用入口 | 聊天记录、记忆接续、消息发送 | 内容浏览、作品发布、直播控制 |
| 资料与版本 | 接入版本、资料状态、来源范围及待核对事项 | 旧会话档案、历史回执和接入版本 | 应用版本、作品记录和结果核对 |

切换应用时保留应用目录、筛选和详情的标签位置。状态取自各自连接及能力的实际证据，服务就绪、账号已登录和动作已授权各自显示。记忆宫殿仍属于工作站主体。应用资料区提供按原会话跳转的入口，原话和图谱均保留原授权范围。

## Windows 实现与验证范围

- 工作站初始化后启用夜海布局。聊天保留原收发入口与草稿，辅助栏只显示本机会话可见的记忆。窄窗将应用详情切成独立内容区，导航和返回入口仍可用。
- 记忆宫殿从本机主人会话的记忆库分页读取。关系线表示记忆与该会话的来源关系。详情按现有来源校验显示引用和仍可见的更正记录。「约定」筛选当前开放的约定。旧微信会话档案使用各自的账号和历史授权，尚未纳入宫殿的浏览范围。
- 应用中心共用四个详情标签。卡片分别显示服务、账号、授权和接入版本。抖音的登录资料保存状态来自本机登记，实际网页可用性以网页为准。直播文件检查和直播控制保留独立状态。
- 使用隔离的 Windows 服务和 Edge 浏览器验证桌面、窄窗、两应用切换及草稿保留。视觉回归使用合成聊天和记忆，记忆接口另用合成存储验证登录、会话隔离和原话读取。

## GPT Image 提示词

以下为内置 GPT Image 生成与编辑的提示词。输入参考来自项目的蓝发鲸尾女仆、夜海图及潮汐地图。

<details>
<summary>聊天</summary>

```text
Use case: ui-mockup. Draw one crisp high-fidelity Windows desktop app screenshot, landscape 16:10, orthographic and full-bleed, entire app contained in frame. Product name “大肥鱼的家”. Chinese sans-serif typography like Microsoft YaHei with high legibility, large correct page title, no tiny invented text. One consistent aesthetic: calm illustrated night sea, coastal contour lines, midnight #0B1832 and ink #18335A, sea-blue #2C5680, cyan #91DCE7 and pale lilac #C8BEFF accents, text #F1F7FF. Intentional glassmorphism, frosted blue cards with fine light rims, translucent sheets and restrained lighting. Densely readable data uses more opaque tinted glass. Moderate shadow, no glow behind small text. Never use neon, generic gradient dashboard, metallic sci-fi HUD, laptop frame, text outside screenshot, or watermark. Left ~210px glass navigation rail with small blue-haired whale-tail maid avatar based on the supplied synthetic UI reference, compact monoline icons, and exact labels “聊天”, “生活与记忆”, “应用”, “项目工具”, “设置”. Bottom of the rail a small “本机工作站” chip. Main content has its own padded grid, readable desktop spacing and a clear hierarchy. No real account names or real message content. Reference images show the current mascot and sea atmosphere, use only as character and style references. Selected navigation “聊天”. Page title “和大肥鱼聊聊”. A generous frosted chat panel occupies most of the center. Show two short fictional bubbles “今晚整理海边日记。” and “我把那段经历留在记忆宫殿里了。”, with clearly distinct owner and mascot avatar, and bottom composer “和大肥鱼说点什么” and button “发送”. To the right use one taller illustrated glass card with the existing chibi blue-haired whale-tailed maid, standing beside a moonlit sea window, subtle and quiet. Below it a compact frosted card titled “最近记忆” with “海边日记” and “打开记忆宫殿”. Illustration occupies only the right card and does not obscure text. Keep clear title and controls with few decorations.
```

</details>

<details>
<summary>二维记忆宫殿</summary>

```text
Use case: ui-mockup. Draw one crisp high-fidelity Windows desktop app screenshot, landscape 16:10, orthographic and full-bleed, entire app contained in frame. Product name “大肥鱼的家”. Chinese sans-serif typography like Microsoft YaHei with high legibility, large correct page title, no tiny invented text. One consistent aesthetic: calm illustrated night sea, coastal contour lines, midnight #0B1832 and ink #18335A, sea-blue #2C5680, cyan #91DCE7 and pale lilac #C8BEFF accents, text #F1F7FF. Intentional glassmorphism, frosted blue cards with fine light rims, translucent sheets and restrained lighting. Densely readable data uses more opaque tinted glass. Moderate shadow, no glow behind small text. Never use neon, generic gradient dashboard, metallic sci-fi HUD, laptop frame, text outside screenshot, or watermark. Left ~210px glass navigation rail with small blue-haired whale-tail maid avatar based on the supplied synthetic UI reference, compact monoline icons, and exact labels “聊天”, “生活与记忆”, “应用”, “项目工具”, “设置”. Bottom of the rail a small “本机工作站” chip. Main content has its own padded grid, readable desktop spacing and a clear hierarchy. No real account names or real message content. First input is a mood reference for the memory canvas, second input is mascot identity. Selected navigation “生活与记忆”. Main title “记忆宫殿”. Across top show scope chip “示例会话”, filters “人物”, “经历”, “约定”, “兴趣”, and “关系图 / 列表” switch. Main 65% frosted glass canvas contains an actual legible 2D relational graph, fine tide-chart contours over quiet flat blue, 5 simple spaced nodes with thin purposeful connections. Node labels “小岚”, “大肥鱼”, “海边日记”, “周日画明信片”, “夜海”, each only once in graph. One amber-highlighted node “周日画明信片” with visible focus ring, line goes to participants. Right 30% substantial frosted detail sheet “周日画明信片”, with chips “约定” and “示例资料”. Divide the sheet into “原话”, “来源”, “更正记录”; sample lines “周日一起画明信片。” and “原会话 · 周六”. End with a small “返回原会话” button. Use contour lines like the reference but avoid photorealistic 3D topography in the usable UI. All labels simplified Chinese with consistent typography. Strong contrast and legibility.
```

</details>

<details>
<summary>应用中心 · 微信</summary>

```text
Use case: ui-mockup. Draw one crisp high-fidelity Windows desktop app screenshot, landscape 16:10, orthographic and full-bleed, entire app contained in frame. Product name “大肥鱼的家”. Chinese sans-serif typography like Microsoft YaHei with high legibility, large correct page title, no tiny invented text. One consistent aesthetic: calm illustrated night sea, coastal contour lines, midnight #0B1832 and ink #18335A, sea-blue #2C5680, cyan #91DCE7 and pale lilac #C8BEFF accents, text #F1F7FF. Intentional glassmorphism, frosted blue cards with fine light rims, translucent sheets and restrained lighting. Densely readable data uses more opaque tinted glass. Moderate shadow, no glow behind small text. Never use neon, generic gradient dashboard, metallic sci-fi HUD, laptop frame, text outside screenshot, or watermark. Left ~210px glass navigation rail with small blue-haired whale-tail maid avatar based on the supplied synthetic UI reference, compact monoline icons, and exact labels “聊天”, “生活与记忆”, “应用”, “项目工具”, “设置”. Bottom of the rail a small “本机工作站” chip. Main content has its own padded grid, readable desktop spacing and a clear hierarchy. No real account names or real message content. Input images are mascot and sea style references. Selected navigation “应用”. Page title “应用中心”. Under title search “搜索应用” and filters “全部” and “已接入”. Center-left shows exactly two equally sized matching frosted app cards stacked vertically, same structure. Card 1 “微信”, a restrained green double-bubble icon, rows “服务 已运行”, “账号 待登录”, “授权 待确认”, and footer action “管理应用”. Card 2 “抖音”, restrained muted cyan and rose musical-note icon, rows “服务 已运行”, “账号 已登录”, “授权 仅浏览”, same footer “管理应用”. Both are illustrated hypothetical states, small top chip “示例状态”. Right side is one roomy reusable details pane for the selected card “应用详情” and “微信”. Four shared tabs “概览”, “账号与权限”, “应用能力”, “资料与版本”. Selected “应用能力”. Under it a neat uniform list with “聊天记录 待核对”, “记忆接续 待核对”, “消息发送 未授权”. A small status note “各应用按授权范围工作”. App brand accents only in icons, the shell and status badge shapes are shared. One sidebar, one grid and one detail pane, no repeated navigation or competing headers. Clean functional app UI, crisp readable Chinese and realistic scale. Do not imply actual logged-in accounts or available livestreaming.
```

</details>

<details>
<summary>应用中心 · 抖音</summary>

```text
Use case: ui-mockup, precise same-screen variant. Input image is the edit target, a high fidelity Windows desktop application screenshot of 大肥鱼的家 / 应用中心. Return the exact same screen at the same landscape 16:10 ratio, using the same glassmorphism, night sea scene, entire illustration, title bar, left sidebar, font, coordinates, margins, cards, filters and colors. Only show the alternate interaction state after clicking the lower 抖音 card. Remove the bright selected outline around the top 微信 card and place the same bright outline around the lower 抖音 card. Keep both cards and all of their original text unchanged. In the existing right detail panel, keep “应用详情”, all four tab labels and their positions “概览”, “账号与权限”, “应用能力”, “资料与版本”, with “应用能力” still selected. Replace the panel's 微信 icon and heading only with the cyan-pink musical note icon and the text “抖音”. The three equally spaced capability rows in the panel should read exactly “内容浏览” with pill “已授权”, “作品发布” with pill “未授权”, and “直播控制” with pill “待验证”, using coherent line icons. Keep footer “各应用按授权范围工作”. Preserve all other visual details, including top title 应用中心, left avatar, glass card shapes, moon and sea, and simulated status chips. Render correctly legible Simplified Chinese. No extra labels, no private account data, no new footer, no watermark.
```

</details>
