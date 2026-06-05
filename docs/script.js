// apigw landing — vanilla JS, no deps. Three jobs:
//   1. Install-tab switcher + copy-to-clipboard buttons.
//   2. Sticky-nav scrolled state for the subtle bottom border.
//   3. Animated terminal demo + reveal-on-scroll for cards/rows.
// Respects prefers-reduced-motion.

(function () {
  'use strict';
  const $  = (s, r = document) => r.querySelector(s);
  const $$ = (s, r = document) => Array.from(r.querySelectorAll(s));
  const reduceMotion =
    window.matchMedia &&
    window.matchMedia('(prefers-reduced-motion: reduce)').matches;

  // ---------- install tabs ----------
  $$('.tab').forEach((tab) => {
    tab.addEventListener('click', () => {
      const target = tab.dataset.tab;
      $$('.tab').forEach((t) => t.classList.toggle('active', t === tab));
      $$('.install-cmd').forEach((cmd) =>
        cmd.classList.toggle('hidden', cmd.dataset.tab !== target),
      );
    });
  });

  // ---------- copy buttons ----------
  $$('.copy-btn').forEach((btn) => {
    btn.addEventListener('click', async () => {
      let text = '';
      if (btn.dataset.copy) {
        const target = $(btn.dataset.copy);
        text = target ? target.innerText : '';
      } else {
        // Default: copy the visible install command in the same install-body.
        const body = btn.closest('.install-body');
        const visible = body ? $('.install-cmd:not(.hidden)', body) : null;
        text = visible ? visible.innerText : '';
      }
      try {
        await navigator.clipboard.writeText(text.trim());
        flashCopied(btn);
      } catch (e) {
        // Fallback for non-secure contexts: select+copy via a temporary range.
        const tmp = document.createElement('textarea');
        tmp.value = text.trim();
        tmp.style.position = 'fixed';
        tmp.style.opacity = '0';
        document.body.appendChild(tmp);
        tmp.select();
        try { document.execCommand('copy'); flashCopied(btn); } catch (_) {}
        document.body.removeChild(tmp);
      }
    });
  });

  function flashCopied(btn) {
    btn.classList.add('copied');
    const label = $('.copy-label', btn);
    const original = label ? label.textContent : null;
    if (label) label.textContent = 'Copied';
    setTimeout(() => {
      btn.classList.remove('copied');
      if (label && original !== null) label.textContent = original;
    }, 1400);
  }

  // ---------- sticky nav border on scroll ----------
  const nav = $('.nav');
  if (nav) {
    const onScroll = () => nav.classList.toggle('scrolled', window.scrollY > 8);
    onScroll();
    window.addEventListener('scroll', onScroll, { passive: true });
  }

  // ---------- reveal on scroll ----------
  if ('IntersectionObserver' in window && !reduceMotion) {
    const io = new IntersectionObserver(
      (entries) => {
        for (const e of entries) {
          if (e.isIntersecting) {
            e.target.classList.add('in-view');
            io.unobserve(e.target);
          }
        }
      },
      { rootMargin: '0px 0px -10% 0px', threshold: 0.05 },
    );
    $$('.feature, .cmd-row, .stat, .ct-row').forEach((el) => io.observe(el));
  } else {
    $$('.feature, .cmd-row, .stat, .ct-row').forEach((el) =>
      el.classList.add('in-view'),
    );
  }

  // ---------- terminal demo ----------
  // Single script: a sequence of (text, kind, delay) tuples. `kind` decides
  // CSS color: prompt | cmd | out | ok | warn | key. Lines without `\n`
  // append; with `\n` they line-break. `delay` is ms before the next step.
  // The whole thing loops after a pause so the page feels alive without
  // burning CPU.
  const term = $('#terminal-body');
  if (term && !reduceMotion) {
    const script = [
      ['$ ', 'prompt', 60],
      ['sudo apigw install', 'cmd', 700, true],
      ['→ Installing nginx, configuring /etc/apigw/...\n', 'out', 220],
      ['→ Wrote /etc/apigw/config.yaml\n', 'out', 200],
      ['✓ apigw v0.1.0 installed in 1.8s\n', 'ok', 320],
      ['\n', 'out', 60],

      ['$ ', 'prompt', 50],
      ['apigw tls enable letsencrypt --domain api.example.com', 'cmd', 600, true],
      ['→ Solving HTTP-01 challenge...\n', 'out', 280],
      ['→ Issued cert (expires 2026-09-03)\n', 'out', 240],
      ['✓ TLS active. Renewal timer armed.\n', 'ok', 320],
      ['\n', 'out', 60],

      ['$ ', 'prompt', 50],
      ['apigw deploy add hello --repo github.com/me/hello --port 3000', 'cmd', 700, true],
      ['→ Cloning hello@main (depth=1)...\n', 'out', 220],
      ['→ Detected runtime: ', 'out', 80],
      ['node', 'key', 80],
      [' (package.json)\n', 'out', 120],
      ['→ npm ci --omit=dev...\n', 'out', 220],
      ['→ Wrote systemd unit, started hello.service\n', 'out', 200],
      ['✓ Live at https://api.example.com/apps/hello\n', 'ok', 320],
      ['\n', 'out', 60],

      ['$ ', 'prompt', 50],
      ['apigw webhook setup hello', 'cmd', 500, true],
      ['→ Paste this into github.com/me/hello/settings/hooks:\n', 'out', 220],
      ['   URL:    https://api.example.com/webhook/hello\n', 'out', 100],
      ['   Secret: ', 'out', 60],
      ['whsec_•••••••••••••••••', 'key', 120],
      ['\n✓ webhook armed — push to redeploy.\n', 'ok', 360],
      ['\n', 'out', 1800],
    ];

    let line = '';
    let i = 0;
    function wrap(text, kind) {
      const cls = kind ? `term-${kind}` : '';
      const safe = text
        .replace(/&/g, '&amp;')
        .replace(/</g, '&lt;')
        .replace(/>/g, '&gt;');
      return cls ? `<span class="${cls}">${safe}</span>` : safe;
    }
    function typeNext() {
      if (i >= script.length) {
        // Loop after a beat. Clear and start over.
        setTimeout(() => {
          term.innerHTML = '';
          line = '';
          i = 0;
          typeNext();
        }, 2400);
        return;
      }
      const step = script[i++];
      const [text, kind, delay, isType] = step;
      if (isType) {
        // Character-by-character typing for the user-entered command.
        let pos = 0;
        const base = term.innerHTML.replace(/<span class="cursor"><\/span>$/, '');
        const interval = setInterval(() => {
          const slice = text.slice(0, pos + 1);
          term.innerHTML = base + wrap(slice, kind) + '<span class="cursor"></span>';
          pos++;
          if (pos >= text.length) {
            clearInterval(interval);
            term.innerHTML += '\n';
            setTimeout(typeNext, delay);
          }
        }, 30 + Math.random() * 25);
      } else {
        term.innerHTML = term.innerHTML.replace(/<span class="cursor"><\/span>$/, '');
        term.innerHTML += wrap(text, kind);
        // Keep the trailing prompt with a cursor if we just printed a prompt.
        if (kind === 'prompt') {
          term.innerHTML += '<span class="cursor"></span>';
        }
        setTimeout(typeNext, delay);
      }
    }
    typeNext();
  } else if (term) {
    // Reduced-motion: render a static snapshot so the panel isn't blank.
    term.innerHTML =
      '<span class="term-prompt">$ </span><span class="term-cmd">sudo apigw install</span>\n' +
      '<span class="term-ok">✓ apigw v0.1.0 installed in 1.8s</span>\n\n' +
      '<span class="term-prompt">$ </span><span class="term-cmd">apigw deploy add hello --repo github.com/me/hello --port 3000</span>\n' +
      '<span class="term-ok">✓ Live at https://api.example.com/apps/hello</span>';
  }
})();
