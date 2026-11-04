(() => {
  'use strict';
  const svgNS = 'http://www.w3.org/2000/svg';

  function title(item) {
    return (item.assertions || []).map(a => window.WechatMemory.relationLabel(item, a)).join('；') || '人物关系';
  }

  function participants(item) {
    const ids = new Set();
    if (item.subject) ids.add(item.subject);
    // A wish about a relative does not make the relative an actor in the recorded episode.
    // Other named parties belong on the separately verified relationship node.
    if (item.palaceType !== 'relation') return ids;
    for (const assertion of item.assertions || []) {
      if (assertion.subject) ids.add(assertion.subject);
      if (assertion.object_basis !== 'literal' && assertion.object) ids.add(assertion.object);
    }
    return ids;
  }

  // Align both columns' centers with the endpoints in the connecting SVG.
  // Labels remain ordinary keyboard-operable controls at all zoom levels.
  function render(target, items, controls) {
    const people = new Map();
    const fish = Symbol('reflection author');
    const edges = items.map(item => {
      const ids = item.note || item.feeling ? [fish] : [...participants(item)];
      for (const id of ids) if (!people.has(id)) people.set(id, people.size);
      return ids;
    });
    const rows = Math.max(people.size, items.length, 1);
    const board = document.createElement('div');
    board.className = 'palace-chart';
    board.style.setProperty('--chart-rows', rows);
    const peopleColumn = document.createElement('div');
    const notesColumn = document.createElement('div');
    peopleColumn.className = 'palace-chart-column palace-chart-people';
    notesColumn.className = 'palace-chart-column palace-chart-notes';
    const connectors = document.createElementNS(svgNS, 'svg');
    connectors.setAttribute('aria-hidden', 'true');
    connectors.setAttribute('viewBox', '0 0 100 100');
    connectors.setAttribute('preserveAspectRatio', 'none');
    for (const id of people.keys()) {
      const control = document.createElement(id === fish ? 'span' : 'button');
      control.className = 'palace-chart-person';
      control.textContent = id === fish ? '大肥鱼' : controls.personLabel(id);
      if (id !== fish) {
        control.type = 'button';
        control.dataset.personId = id;
        control.setAttribute('aria-pressed', String(controls.personID === id));
        control.addEventListener('click', () => controls.selectPerson(id));
      }
      peopleColumn.append(control);
    }
    items.forEach((item, index) => {
      controls.createEntry(notesColumn, item, 'memory-palace-node');
      for (const id of edges[index]) {
        const from = (people.get(id) + 0.5) * 100 / people.size;
        const to = (index + 0.5) * 100 / items.length;
        const curve = document.createElementNS(svgNS, 'path');
        curve.setAttribute('d', 'M 0 '+from+' C 50 '+from+' 50 '+to+' 100 '+to);
        if (id !== fish) curve.dataset.person = id;
        curve.dataset.record = item.palaceKey;
        curve.setAttribute('vector-effect', 'non-scaling-stroke');
        connectors.append(curve);
      }
    });
    board.append(peopleColumn, connectors, notesColumn);
    target.replaceChildren(board);
  }

  window.WechatPalaceGraph = {render, title, participants};
})();
