(() => {
  const preference = matchMedia('(prefers-color-scheme: dark)');
  let mode = 'auto';
  try { const saved = localStorage.getItem('cfsm-theme'); if (['auto','light','dark'].includes(saved)) mode = saved; } catch {}
  function apply() {
    document.documentElement.dataset.theme = mode === 'auto' ? (preference.matches ? 'dark' : 'light') : mode;
    const button = document.getElementById('theme-toggle');
    if (button) {
      button.textContent = {auto:'◐',light:'☀︎',dark:'☾'}[mode];
      button.title = '主题：' + {auto:'跟随系统',light:'浅色',dark:'暗色'}[mode];
      button.setAttribute('aria-label', button.title + '，点击切换');
    }
    document.dispatchEvent(new Event('themechange'));
  }
  apply();
  document.addEventListener('DOMContentLoaded', () => {
    document.getElementById('theme-toggle')?.addEventListener('click', () => {
      mode = {auto:'dark',dark:'light',light:'auto'}[mode];
      try { localStorage.setItem('cfsm-theme',mode); } catch {}
      apply();
    });
    apply();
  });
  preference.addEventListener('change', () => { if (mode === 'auto') apply(); });
  window.addEventListener('storage', (e) => { if (e.key === 'cfsm-theme') { mode=['auto','light','dark'].includes(e.newValue)?e.newValue:'auto';apply(); } });
})();
