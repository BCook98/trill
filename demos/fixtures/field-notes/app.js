(() => {
  const storageKey = 'field-notes:bookmarks';
  const buttons = [...document.querySelectorAll('.bookmark')];
  const storyIds = new Set(buttons.map(button => button.dataset.story));
  const status = document.querySelector('#status');
  let saved = new Set();

  function readSaved() {
    const value = JSON.parse(localStorage.getItem(storageKey) || '[]');
    return new Set(Array.isArray(value) ? value.filter(id => storyIds.has(id)) : []);
  }

  function render() {
    buttons.forEach(button => {
      button.setAttribute('aria-pressed', String(saved.has(button.dataset.story)));
    });
  }

  try {
    saved = readSaved();
  } catch {
    // Bookmarks still work for this visit if storage is unavailable or malformed.
  }
  render();

  buttons.forEach(button => {
    button.hidden = false;
    button.addEventListener('click', () => {
      const id = button.dataset.story;
      const isSaved = !saved.has(id);
      if (isSaved) saved.add(id);
      else saved.delete(id);
      render();

      const title = button.closest('.card').querySelector('h2').textContent;
      try {
        localStorage.setItem(storageKey, JSON.stringify([...saved]));
        status.textContent = `${isSaved ? 'Bookmarked' : 'Bookmark removed'}: ${title}`;
      } catch {
        status.textContent = `${isSaved ? 'Bookmarked' : 'Bookmark removed'}: ${title}. Storage unavailable; changes last for this visit.`;
      }
    });
  });

  window.addEventListener('storage', event => {
    if (event.key !== storageKey && event.key !== null) return;
    try {
      saved = readSaved();
      render();
    } catch {
      // Keep the current state if storage cannot be read.
    }
  });
})();
