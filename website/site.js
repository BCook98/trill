/* Duo's two real clips, one automatic presentation, no player settings. */
(() => {
  const root = document.getElementById('demo');
  if (!root) return;
  const $ = id => document.getElementById(id);
  const cover = $('cover-video'), open = $('open-video');
  const stage = $('demo-stage'), toggle = $('demo-toggle');
  const appearance = matchMedia('(prefers-color-scheme: dark)');
  const reducedMotion = matchMedia('(prefers-reduced-motion: reduce)');
  let manifest, phase = 'cover', playing = false, finished = false;
  let manualPause = reducedMotion.matches, blocked = false, generation = 0;
  let foldProgress = 0, frame = 0, lastTick = 0;
  let theme = appearance.matches ? 'dark' : 'light';
  const clip = () => manifest.duo[theme];
  const current = () => phase === 'open' ? open : cover;
  const visible = () => {
    const bounds = stage.getBoundingClientRect();
    return !document.hidden && bounds.bottom > 0 && bounds.top < innerHeight;
  };
  function fold(progress) {
    foldProgress = progress;
    root.style.setProperty('--base-opacity', progress === 0 ? '0' : '1');
    root.style.setProperty('--fold', `${-180 + 180 * progress}deg`);
    root.style.setProperty('--shift', `${25 * (1 - progress)}%`);
    root.style.setProperty('--open-opacity', progress === 1 ? '1' : '0');
  }
  function label() {
    const text = finished ? 'Replay' : playing ? 'Pause' : 'Play';
    toggle.setAttribute('aria-label', `${text} demo`);
    $('demo-toggle-label').textContent = text;
    toggle.toggleAttribute('data-prompt', blocked || (manualPause && !finished));
    root.dataset.phase = phase;
  }
  function stop() {
    generation++;
    playing = false;
    cover.pause(); open.pause(); cancelAnimationFrame(frame); lastTick = 0;
    label();
  }
  function tick(now) {
    if (!playing) return;
    const elapsed = lastTick ? now - lastTick : 0;
    lastTick = now;
    fold(Math.min(1, foldProgress + elapsed / 1400));
    if (foldProgress === 1) {
      phase = 'open'; playing = false; lastTick = 0;
      $('demo-status').textContent = 'Files and preview, beside your session.';
      play();
    } else frame = requestAnimationFrame(tick);
  }
  async function play() {
    if (!manifest || manualPause || finished || !visible() || playing) return;
    playing = true; blocked = false;
    const ticket = ++generation;
    label();
    if (phase === 'hinge') {
      if (reducedMotion.matches) fold(1);
      frame = requestAnimationFrame(tick);
      return;
    }
    const video = current();
    try {
      await video.play();
      if (ticket !== generation) return;
    } catch (error) {
      if (ticket !== generation) return;
      playing = false; blocked = true;
      $('demo-status').textContent = error.name === 'NotAllowedError'
        ? 'Tap to watch the demo.' : 'Recording paused. Tap to retry.';
      label();
    }
  }
  function configure() {
    for (const [name, video] of [['cover', cover], ['open', open]]) {
      video.muted = true;
      video.poster = clip()[name].poster;
      video.src = clip()[name].src;
    }
    $('inner-left-poster').src = clip().open.poster;
    $('inner-right-poster').src = clip().open.poster;
    root.dataset.theme = theme;
  }
  function start() {
    stop(); phase = 'cover'; finished = false; fold(0);
    cover.currentTime = 0; open.currentTime = 0;
    $('demo-status').textContent = 'Opening a session on your Mac.';
    play(); label();
  }
  cover.addEventListener('ended', () => {
    if (!playing || phase !== 'cover') return;
    playing = false; phase = 'hinge';
    $('demo-status').textContent = 'A wider view on Duo.';
    play();
  });
  open.addEventListener('ended', () => {
    if (phase !== 'open') return;
    finished = true; stop();
    $('demo-status').textContent = 'Your work stays on your Mac.';
  });
  for (const video of [cover, open]) video.addEventListener('error', () => {
    if (video !== current() || phase === 'hinge') return;
    stop(); blocked = true;
    $('demo-status').textContent = 'Recording paused. Tap to retry.';
    label();
  });
  toggle.addEventListener('click', () => {
    if (playing) { manualPause = true; stop(); return; }
    manualPause = false; blocked = false;
    if (finished) start();
    else {
      if (current().error) current().load();
      play();
    }
  });
  appearance.addEventListener('change', () => {
    if (!manifest) return;
    // Match the same part of the workflow, accounting for different clip lengths.
    const previousPhase = phase;
    const progress = phase === 'hinge' ? 0 : current().currentTime / clip()[phase].duration;
    stop(); theme = appearance.matches ? 'dark' : 'light'; configure();
    const ticket = generation;
    const video = current();
    const resume = () => {
      if (ticket !== generation) return;
      if (previousPhase !== 'hinge') video.currentTime = Math.min(progress, 1) * clip()[previousPhase].duration;
      play();
    };
    video.addEventListener('loadedmetadata', resume, { once: true });
    video.load();
  });
  reducedMotion.addEventListener('change', () => {
    if (!reducedMotion.matches) return;
    manualPause = true; stop();
    if (phase === 'hinge') { phase = 'open'; fold(1); }
    label();
  });
  function visibility() {
    if (!visible()) stop();
    else if (!blocked) play();
  }
  document.addEventListener('visibilitychange', visibility);
  addEventListener('pagehide', stop);
  addEventListener('pageshow', visibility);
  if ('IntersectionObserver' in window) new IntersectionObserver(visibility).observe(stage);
  else addEventListener('scroll', visibility, { passive: true });
  fetch('/assets/demos/manifest.json').then(response => {
    if (!response.ok) throw new Error('Metadata unavailable');
    return response.json();
  }).then(data => {
    manifest = data; configure(); toggle.hidden = false;
    if (reducedMotion.matches) {
      phase = 'open'; fold(1);
      open.poster = clip().open.previewPoster;
      $('demo-status').textContent = 'Reduced motion. Play when you’re ready.';
    }
    label(); play();
  }).catch(() => {
    $('demo-status').textContent = 'The demo couldn’t load. Reload the page to try again.';
  });
})();
