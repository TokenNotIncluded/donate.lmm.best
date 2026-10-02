(() => {
  "use strict";

  const logos = [...document.querySelectorAll("pre.coffee-logo")];
  if (!logos.length) return;

  const steam = [
    ["   )  (    ", "    (  )   "],
    ["   )  )    ", "   (   )   "],
    ["    ( )    ", "   (  (    "],
    ["    (  (   ", "  )   (    "],
    ["   )   (   ", "  )    )   "],
    ["  )   )    ", "   (   )   "],
    ["  (   )    ", "   (  (    "],
    ["   (  (    ", "    ) (    "],
  ];
  const cup = ["  .----.   ", "  |    |)  ", "  '----'   ", " --------  "];
  const parents = logos.map(logo => logo.closest("button, a"));
  const reducedMotion = window.matchMedia("(prefers-reduced-motion: reduce)");
  let frame = 0;
  let timer = null;

  const draw = () => {
    const art = [...steam[frame], ...cup].join("\n");
    for (const logo of logos) logo.textContent = art;
  };

  const tick = () => {
    frame = (frame + 1) % steam.length;
    draw();
    const active = parents.some(parent => parent && (
      parent === document.activeElement || parent.matches(":hover")
    ));
    timer = window.setTimeout(tick, active ? 200 : 260);
  };

  const sync = () => {
    window.clearTimeout(timer);
    timer = null;
    if (reducedMotion.matches) {
      frame = 0;
      draw();
    } else if (!document.hidden) {
      timer = window.setTimeout(tick, 260);
    }
  };

  draw();
  document.addEventListener("visibilitychange", sync);
  reducedMotion.addEventListener("change", sync);
  sync();
})();
