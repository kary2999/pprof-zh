// pprof Web UI 汉化：精确匹配 + 正则匹配；未命中的文案原样保留（回退英文）。
(function () {
  const EXACT = {
    // 菜单
    'View': '视图', 'Top': '列表', 'Graph': '调用图', 'Flame Graph': '火焰图',
    'Peek': '上下游', 'Source': '源码', 'Disassemble': '反汇编',
    'Sample': '采样类型', 'Refine': '筛选', 'Config': '配置',
    'Focus': '聚焦', 'Ignore': '忽略', 'Hide': '隐藏', 'Show': '显示',
    'Show from': '显示起点', 'Reset': '重置',
    'Save as ...': '另存为…', 'Download': '下载', 'Help\u00a0\u2907': '帮助 \u2907', 'Help ⤇': '帮助 ⤇',
    // 对话框
    'Save options as': '配置另存为', 'New config': '新配置名',
    'Cancel': '取消', 'Save': '保存', 'Delete': '删除', 'Delete config': '删除配置',
    'Save failed': '保存失败', 'Delete failed': '删除失败',
    // Top 表头
    'Flat': '自身', 'Flat%': '自身%', 'Sum%': '累计%', 'Cum': '含子调用', 'Cum%': '含子调用%',
    'Name': '函数名', 'Inlined?': '内联?', '(inline)': '(内联)',
    // 采样类型（Sample 菜单项）
    'samples': '采样数', 'cpu': 'CPU 时间', 'alloc_objects': '累计分配对象数', 'alloc_space': '累计分配空间',
    'inuse_objects': '在用对象数', 'inuse_space': '在用空间', 'contentions': '竞争次数', 'delay': '延迟',
    // 提示
    'Show information about the profile and this view': '显示 profile 与当前视图的详细信息',
    'Display profile as a directed graph': '以有向图展示 profile（需要 Graphviz）',
    'Display profile as a flame graph': '以火焰图展示 profile',
    'Show the entire profile': '显示完整 profile',
    'Save current settings': '保存当前设置',
    'Outputs top entries in text form': '以文本列表展示耗时最高的条目',
    'Output callers/callees of functions matching regexp': '展示匹配函数的调用方与被调用方',
    'Output annotated source for functions matching regexp': '展示匹配函数的带采样标注的源码',
    'Output assembly listings annotated with samples': '展示带采样标注的反汇编（需要 objdump）',
    'Restricts the profile to samples whose stack contains the matching function': '只保留调用栈中含匹配函数的采样',
    'Profile documentation': 'Profile 文档',
    'Search regexp': '搜索（正则）',
    'Total': '合计',
    'Show source code': '显示源码', 'Show source in new tab': '在新标签页显示源码',
    'Copy params from this page': '复制本页参数',
  };
  // 動態文案（含數值），按前綴/模式翻
  const RULES = [
    [/^(Focus|Ignore|Hide|Show|Show from) expression matched no samples$/, '“$1”条件没有匹配到任何采样。提示：CPU profile 只含占用 CPU 的函数，等待 IO / sleep 的函数不会出现，可用顶部的“重置全部筛选”清除条件。'],
    [/^Type: /, '类型: '], [/^Time: /, '时间: '],
    [/^Showing nodes accounting for (.+?), (.+?) of (.+?) total$/, '显示节点合计 $1（$2），总量 $3'],
    [/^Showing top (\d+) nodes out of (\d+)$/, '显示前 $1 个节点（共 $2 个）'],
    [/^Duration: (.+?), Total samples = (.+)$/, '时长: $1，采样总量 = $2'],
    [/^Dropped (\d+) nodes \(cum <= (.+)\)$/, '已省略 $1 个节点（含子调用 <= $2）'],
    [/^Build ID: /, '构建 ID: '], [/^File: /, '文件: '], [/^Delete (.+)\?$/, '删除“$1”？'],
    [/^(.+) \(inlined\)$/, '$1（内联）'],
  ];
  const SKIP = 'svg,script,style,pre,#graph,.line,.filename,h2,#stack-chart,#stack-holder';

  function tr(s) {
    if (!s) return s;
    const t = s.trim();
    if (!t) return s;
    let r = EXACT[t];
    if (r === undefined) {
      for (const [re, to] of RULES) if (re.test(t)) { r = t.replace(re, to); break; }
    }
    return r === undefined ? s : s.replace(t, r);
  }
  function walk(root) {
    if (root.nodeType === 3) { fixText(root); return; }
    if (root.nodeType !== 1 || root.closest && root.closest(SKIP)) return;
    for (const a of ['title', 'placeholder']) {
      if (root.hasAttribute(a)) { const v = tr(root.getAttribute(a)); if (v !== root.getAttribute(a)) root.setAttribute(a, v); }
    }
    for (const n of root.childNodes) walk(n);
  }
  function fixText(n) {
    const p = n.parentElement;
    if (p && p.closest(SKIP)) return;
    const v = tr(n.nodeValue);
    if (v !== n.nodeValue) n.nodeValue = v;
  }
  let busy = false;
  const mo = new MutationObserver(ms => {
    if (busy) return; busy = true;
    for (const m of ms) {
      if (m.type === 'characterData') fixText(m.target);
      else m.addedNodes.forEach(walk);
    }
    busy = false;
  });

  // ===== 导航工具条：后退 / 适应屏幕 / 清除选中 / 筛选标签 =====
  function initToolbar() {
    const header = document.querySelector('.header');
    if (!header || !document.getElementById('view')) return; // 仅 pprof 主页面
    if (!window.PZ_FULL) { // 上下游 / 源码 / 反汇编依赖源文件与可执行文件，通常没有输出，默认隐藏（PPROF_ZH_FULL=1 可恢复）
      ['peek', 'list', 'disasm'].forEach(id => { const e = document.getElementById(id); if (e) e.remove(); });
    }
    if (window.PZ_CALLS) {
      const sub = document.querySelector('#view .submenu');
      if (sub) { const a = document.createElement('a'); a.href = './calls'; a.textContent = '调用统计'; sub.appendChild(a); }
    }
    const bar = document.createElement('div');
    bar.id = 'pz-bar';
    const mk = (txt, title, fn) => { const b = document.createElement('button'); b.textContent = txt; b.title = title; b.onclick = fn; bar.appendChild(b); return b; };
    mk('← 后退', '回到上一个视图 / 上一步筛选（等同浏览器后退）', () => history.back());
    const svg = document.querySelector('#graph svg');
    if (svg) mk('⤢ 适应屏幕', '把缩放和位置恢复到初始状态', () => resetZoom(svg));
    if (svg || document.querySelector('#top')) mk('✕ 清除选中', '取消所有已选中的节点（快捷键 Esc）', clearSelection);
    // 当前生效的筛选条件 -> 可单独移除的标签
    const names = { f: '聚焦', i: '忽略', h: '隐藏', s: '显示', sf: '显示起点' };
    const url = new URL(location.href);
    let any = false;
    for (const k of Object.keys(names)) {
      const v = url.searchParams.get(k);
      if (!v) continue;
      any = true;
      const chip = document.createElement('span');
      chip.className = 'pz-chip';
      chip.title = v;
      chip.textContent = names[k] + '：' + (v.length > 28 ? v.slice(0, 28) + '…' : v) + '  ✕';
      chip.onclick = () => { const u = new URL(location.href); u.searchParams.delete(k); location.href = u.toString(); };
      bar.appendChild(chip);
    }
    if (any) mk('重置全部筛选', '清除所有筛选条件，显示完整 profile', () => { const u = new URL(location.href); for (const k of Object.keys(names)) u.searchParams.delete(k); location.href = u.toString(); });
    else { const h = document.createElement('span'); h.className = 'pz-hint'; h.textContent = '提示：点击节点选中，再用「筛选」菜单聚焦/隐藏；滚轮缩放，拖拽平移'; bar.appendChild(h); }
    header.insertAdjacentElement('afterend', bar);
    document.addEventListener('keydown', e => { if (e.key === 'Escape' && !/INPUT|TEXTAREA/.test((e.target || {}).nodeName || '')) clearSelection(); });
  }
  // 通过模拟滚轮把 pprof 内部缩放状态也带回 1.0，再恢复平移位置，避免状态错位
  const initVB = {};
  function resetZoom(svg) {
    const vb = svg.viewBox.baseVal;
    if (!initVB.w) return;
    const s = initVB.w / vb.width;
    const n = Math.round(Math.log(s) / Math.log(1.1));
    for (let i = 0; i < Math.abs(n); i++) svg.dispatchEvent(new WheelEvent('wheel', { deltaY: n > 0 ? 1 : -1, bubbles: true, cancelable: true }));
    vb.x = initVB.x; vb.y = initVB.y; vb.width = initVB.w; vb.height = initVB.h;
  }
  function clearSelection() {
    // 调用图：被选中的节点多边形填充为 #ccccff；模拟一次点击即可取消
    document.querySelectorAll('#graph polygon').forEach(p => {
      if ((p.style.fill || '').replace(/\s/g, '').toLowerCase() === '#ccccff' || p.style.fill === 'rgb(204, 204, 255)') {
        const o = { bubbles: true, cancelable: true, button: 0 };
        p.dispatchEvent(new MouseEvent('mousedown', o)); p.dispatchEvent(new MouseEvent('mouseup', o));
      }
    });
    // 列表：被选中的行带 hilite 类
    document.querySelectorAll('#top tr.hilite td:first-child').forEach(td => td.dispatchEvent(new MouseEvent('click', { bubbles: true, cancelable: true })));
  }
  function captureInit() {
    const svg = document.querySelector('#graph svg');
    if (svg && !initVB.w) { const v = svg.viewBox.baseVal; Object.assign(initVB, { x: v.x, y: v.y, w: v.width, h: v.height }); }
  }

  // ===== 下拉菜单：再点一次表头收起 / Esc 收起 / 鼠标移出后自动收起 =====
  function initMenuClose() {
    const subs = () => document.querySelectorAll('.menu-item .submenu');
    const closeAll = () => subs().forEach(m => { m.style.display = 'none'; });
    document.addEventListener('mousedown', e => {
      const hdr = e.target.closest && e.target.closest('.menu-item');
      if (!hdr) return;
      const sub = hdr.querySelector('.submenu');
      if (sub && sub.style.display === 'block' && e.target.parentElement === hdr) {
        sub.style.display = 'none';
        e.stopPropagation(); // 阻止 pprof 自带的“打开”处理再次展开
      }
    }, true);
    document.addEventListener('keydown', e => { if (e.key === 'Escape') closeAll(); });
    document.querySelectorAll('.menu-item').forEach(item => {
      let t = null;
      item.addEventListener('mouseleave', () => { t = setTimeout(() => { const m = item.querySelector('.submenu'); if (m) m.style.display = 'none'; }, 500); });
      item.addEventListener('mouseenter', () => clearTimeout(t));
    });
  }
  // ===== 列表页增加“调用次数”列（数据来自 /ui/calls.json，只有被插桩的函数才有值） =====
  function initTopCalls() {
    const rows = document.getElementById('rows');
    const head = document.querySelector('#toptable thead tr');
    if (!window.PZ_CALLS || !rows || !head) return;
    fetch('./calls.json').then(r => r.json()).then(m => {
      const th = document.createElement('th'); th.textContent = '调用次数'; th.title = '来自插桩统计；标准库等未插桩的函数显示 -';
      head.appendChild(th);
      const fill = () => rows.querySelectorAll('tr').forEach(tr => {
        if (tr.dataset.pzCalls) return;
        tr.dataset.pzCalls = '1';
        const name = tr.cells[5] && tr.cells[5].textContent.trim();
        const td = document.createElement('td');
        td.textContent = m[name] ? m[name].calls.toLocaleString() : '-';
        td.style.textAlign = 'right';
        tr.appendChild(td);
      });
      fill();
      new MutationObserver(fill).observe(rows, { childList: true }); // 点表头重新排序时补列
    }).catch(() => {});
  }
  function start() {
    initTopCalls();
    initMenuClose();
    captureInit();
    initToolbar();
    walk(document.body);
    mo.observe(document.body, { childList: true, subtree: true, characterData: true });
  }
  document.readyState === 'loading' ? document.addEventListener('DOMContentLoaded', start) : start();
})();
