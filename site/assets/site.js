// Copy buttons on code blocks, a collapsed nav on small screens, and
// highlighting of the current section in the page outline.
(function () {
  document.querySelectorAll('pre').forEach(function (pre) {
    if (!navigator.clipboard) return;
    var b = document.createElement('button');
    b.type = 'button';
    b.className = 'copy';
    b.textContent = 'Copy';
    b.addEventListener('click', function () {
      navigator.clipboard.writeText(pre.querySelector('code').innerText).then(function () {
        b.textContent = 'Copied';
        setTimeout(function () { b.textContent = 'Copy'; }, 1500);
      });
    });
    pre.appendChild(b);
  });

  var nav = document.querySelector('.navwrap');
  if (nav && window.matchMedia('(max-width: 860px)').matches) nav.removeAttribute('open');

  var links = document.querySelectorAll('.outline a');
  if (!links.length || !('IntersectionObserver' in window)) return;
  var byId = {};
  links.forEach(function (a) { byId[a.getAttribute('href').slice(1)] = a; });
  var obs = new IntersectionObserver(function (entries) {
    entries.forEach(function (e) {
      if (!e.isIntersecting) return;
      links.forEach(function (a) { a.classList.remove('active'); });
      var a = byId[e.target.id];
      if (a) a.classList.add('active');
    });
  }, { rootMargin: '-60px 0px -70% 0px' });
  Object.keys(byId).forEach(function (id) {
    var h = document.getElementById(id);
    if (h) obs.observe(h);
  });
})();
