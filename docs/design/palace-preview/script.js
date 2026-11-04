(() => {
  const nodes = {
    plan: {
      type: "约定", title: "周六一起画海边明信片",
      body: "小林周四提出了这件事。周五已经改期，现在约好的是周日。",
      sources: [["小林 · 周四 20:14", "周六一起画海边明信片吧。"]]
    },
    revision: {
      type: "修订", title: "日期从周六改成周日",
      body: "大肥鱼提出改期，小林同意。周日是这份约定当前有效的日期。",
      sources: [["大肥鱼 · 周五 09:18", "我周六临时有事，改周日可以吗？"], ["小林 · 周五 09:20", "可以，周日见。"]]
    },
    wave: {
      type: "实际看过", title: "一条关于海浪的观察",
      body: "小林分享了海浪片段，大肥鱼看过其中的退潮画面。",
      sources: [["小林 · 周五 21:05 · 分享", "你看，退潮了。"], ["画面观察 · 片段 0–8 秒", "水从礁石边退下，石头间留下几处水洼。"]]
    },
    interest: {
      type: "兴趣", title: "还想再看看潮间带",
      body: "这是大肥鱼看过退潮片段后，在手记里记下的兴趣。",
      sources: [["大肥鱼 · 周五 21:08 · 手记", "还想看看，退潮后那些小水洼里有什么。"]]
    }
  };
  const root = document.querySelector(".shell");
  const select = (query) => document.querySelector(query);

  function switchMode(mode) {
    document.querySelectorAll("[data-view]").forEach((view) => { view.hidden = view.dataset.view !== mode; });
    document.querySelectorAll(".mode-button").forEach((button) => {
      const active = button.dataset.mode === mode;
      button.classList.toggle("is-active", active);
      button.setAttribute("aria-pressed", String(active));
    });
  }

  function chooseNode(id) {
    const node = nodes[id];
    if (!node) return;
    document.querySelectorAll(".map-node").forEach((element) => element.classList.toggle("selected", element.dataset.node === id));
    select("#evidence-type").textContent = node.type;
    select("#evidence-title").textContent = node.title;
    select("#evidence-body").textContent = node.body;
    const sources = select("#source-stack");
    sources.replaceChildren(...node.sources.map(([who, quote]) => {
      const item = document.createElement("p");
      const speaker = document.createElement("b");
      item.className = "source-chip";
      speaker.textContent = who;
      item.append(speaker, quote);
      return item;
    }));
  }

  function toggleList(button) {
    const list = select("#source-list");
    const open = list.hidden;
    list.hidden = !open;
    button.setAttribute("aria-expanded", String(open));
    button.innerHTML = `${open ? "收起所有片段" : "以列表查看所有片段"} <span aria-hidden="true">${open ? "↑" : "↓"}</span>`;
    if (!open || list.childElementCount) return;
    Object.entries(nodes).forEach(([id, item]) => {
      const row = document.createElement("li");
      const control = document.createElement("button");
      control.type = "button";
      control.dataset.listNode = id;
      control.textContent = `${item.type}：${item.title}`;
      row.append(control);
      list.append(row);
    });
  }

  function toggleScene(button) {
    const atlas = root.classList.toggle("scene-atlas");
    const image = select(".hero-image img");
    image.src = image.dataset[atlas ? "palaceSrc" : "journalSrc"];
    image.alt = atlas ? "潮汐观测站的精密海图仪表" : "夜晚的海面与远处微光";
    button.textContent = `场景：${atlas ? "潮汐观测站" : "夜海手记"}`;
    button.setAttribute("aria-pressed", String(atlas));
    button.title = atlas ? "切换为夜海手记" : "切换为潮汐观测站";
    select(".hero-image figcaption").textContent = atlas ? "场景插画 · 潮汐观测站" : "场景插画 · 夜海手记";
  }

  document.addEventListener("click", (event) => {
    const mode = event.target.closest("[data-mode]");
    if (mode) { switchMode(mode.dataset.mode); if (mode.dataset.node) chooseNode(mode.dataset.node); }
    const mapNode = event.target.closest(".map-node");
    if (mapNode) chooseNode(mapNode.dataset.node);
    const listButton = event.target.closest(".list-toggle");
    if (listButton) toggleList(listButton);
    const listNode = event.target.closest("[data-list-node]");
    if (listNode) chooseNode(listNode.dataset.listNode);
    const sceneButton = event.target.closest(".scene-button");
    if (sceneButton) toggleScene(sceneButton);
  });
  document.querySelectorAll(".map-node").forEach((element) => {
    element.addEventListener("keydown", (event) => {
      if (event.key === "Enter" || event.key === " ") { event.preventDefault(); chooseNode(element.dataset.node); }
    });
  });
  chooseNode("plan");
})();
