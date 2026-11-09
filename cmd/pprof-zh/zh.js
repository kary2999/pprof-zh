// pprof Web UI 漢化：精確匹配 + 正則匹配；未命中的文案原樣保留（回退英文）。
(function () {
  // 中文詞更寬，防止選單被擠成豎排
  const st = document.createElement('style');
  st.textContent = '.menu-name,.menu-item a,.submenu a{white-space:nowrap} .header{flex-wrap:wrap}';
  document.head.appendChild(st);
  const EXACT = {
    // 選單
    'View': '視圖', 'Top': '列表', 'Graph': '調用圖', 'Flame Graph': '火焰圖',
    'Peek': '上下游', 'Source': '源碼', 'Disassemble': '反匯編',
    'Sample': '採樣類型', 'Refine': '篩選', 'Config': '配置',
    'Focus': '聚焦', 'Ignore': '忽略', 'Hide': '隱藏', 'Show': '顯示',
    'Show from': '顯示起點', 'Reset': '重置',
    'Save as ...': '另存為…', 'Download': '下載', 'Help ⤇': '幫助 ⤇', 'Help ⤇': '幫助 ⤇',
    // 對話框
    'Save options as': '配置另存為', 'New config': '新配置名',
    'Cancel': '取消', 'Save': '保存', 'Delete': '刪除', 'Delete config': '刪除配置',
    'Save failed': '保存失敗', 'Delete failed': '刪除失敗',
    // Top 表頭
    'Flat': '自身', 'Flat%': '自身%', 'Sum%': '累計%', 'Cum': '含子調用', 'Cum%': '含子調用%',
    'Name': '函數名', 'Inlined?': '內聯?',
    // title 提示
    'Show information about the profile and this view': '顯示 profile 與當前視圖的詳細信息',
    'Display profile as a directed graph': '以有向圖展示 profile',
    'Display profile as a flame graph': '以火焰圖展示 profile',
    'Show the entire profile': '顯示完整 profile',
    'Save current settings': '保存當前設置',
    'Outputs top entries in text form': '以文本列表展示耗時最高的條目',
    'Output callers/callees of functions matching regexp': '展示匹配函數的調用方與被調用方',
    'Output annotated source for functions matching regexp': '展示匹配函數的帶採樣標註的源碼',
    'Output assembly listings annotated with samples': '展示帶採樣標註的反匯編',
    'Profile documentation': 'Profile 文檔',
    'Search regexp': '搜索（正則）',
    'Total': '合計',
    'Show source code': '顯示源碼', 'Show source in new tab': '在新分頁顯示源碼',
  };
  // 動態文案（含數值），按前綴/模式翻
  const RULES = [
    [/^Type: /, '類型: '], [/^Time: /, '時間: '], [/^Showing nodes accounting for (.+?), (.+?) of (.+?) total$/, '顯示節點合計 $1（$2），總量 $3'],
    [/^Showing top (\d+) nodes out of (\d+)$/, '顯示前 $1 個節點（共 $2 個）'],
    [/^Duration: (.+?), Total samples = (.+)$/, '時長: $1，採樣總量 = $2'],
    [/^Dropped (\d+) nodes \(cum <= (.+)\)$/, '已省略 $1 個節點（含子調用 <= $2）'],
    [/^Build ID: /, '構建 ID: '], [/^File: /, '文件: '], [/^Delete (.+)\?$/, '刪除「$1」？'],
    [/^(.+) \(inlined\)$/, '$1（內聯）'],
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
  function start() {
    walk(document.body);
    mo.observe(document.body, { childList: true, subtree: true, characterData: true });
  }
  document.readyState === 'loading' ? document.addEventListener('DOMContentLoaded', start) : start();
})();
