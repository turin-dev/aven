const reduced = matchMedia('(prefers-reduced-motion: reduce)').matches;
const wait = (ms) => new Promise((r) => setTimeout(r, ms));

// Copy buttons.
document.querySelectorAll('[data-copy]').forEach((button) => {
    const label = button.textContent;
    button.addEventListener('click', async () => {
        try {
            await navigator.clipboard.writeText(button.dataset.copy);
            button.textContent = button.dataset.done;
            setTimeout(() => { button.textContent = label; }, 1600);
        } catch {
            getSelection().selectAllChildren(button.parentElement.querySelector('code'));
        }
    });
});

// Nav border once the page scrolls.
const nav = document.querySelector('.nav');
addEventListener('scroll', () => nav.classList.toggle('scrolled', scrollY > 8), { passive: true });

// Call fn once when el scrolls into view.
const onView = (el, fn, threshold = 0.25) => {
    if (!('IntersectionObserver' in window)) return fn();
    const io = new IntersectionObserver(([e]) => {
        if (e.isIntersecting) { io.disconnect(); fn(); }
    }, { threshold });
    io.observe(el);
};

document.querySelectorAll('[data-reveal]').forEach((el) => onView(el, () => el.classList.add('in'), 0.12));

// Hero canvas: wires from the main card to each service, deploy states, traffic dots, drag to move.
(() => {
    const stage = document.querySelector('[data-stage]');
    if (!stage) return;
    const svg = stage.querySelector('.links');
    const core = stage.querySelector('[data-core]');
    const sats = [...stage.querySelectorAll('[data-sat]')];
    const NS = 'http://www.w3.org/2000/svg';
    // Between phone and wide desktop the sides are too narrow: park the services above and below the card.
    if (innerWidth < 1200) {
        sats.forEach((sat) => {
            const [x, y] = sat.dataset.mid.split(',');
            sat.style.left = `${x}%`;
            sat.style.top = `${y}%`;
        });
    }
    const wires = sats.map(() => {
        const path = document.createElementNS(NS, 'path');
        svg.appendChild(path);
        return path;
    });

    const center = (el) => {
        const r = el.getBoundingClientRect();
        const s = stage.getBoundingClientRect();
        return [r.left - s.left + r.width / 2, r.top - s.top + r.height / 2];
    };
    const layout = () => {
        const { width, height } = stage.getBoundingClientRect();
        svg.setAttribute('viewBox', `0 0 ${width} ${height}`);
        const [x1, y1] = center(core);
        sats.forEach((sat, i) => {
            const [x2, y2] = center(sat);
            const mx = (x1 + x2) / 2;
            wires[i].setAttribute('d', `M${x1} ${y1} C${mx} ${y1} ${mx} ${y2} ${x2} ${y2}`);
        });
    };
    layout();
    addEventListener('resize', layout);
    document.fonts?.ready.then(layout);

    // Draw each wire in, then take its service through building → deploying → online.
    const drawIn = (path, delay) => {
        const len = path.getTotalLength();
        path.style.strokeDasharray = `${len}`;
        path.style.strokeDashoffset = `${len}`;
        path.style.transition = `stroke-dashoffset 0.9s ease ${delay}ms`;
        requestAnimationFrame(() => { path.style.strokeDashoffset = '0'; });
        setTimeout(() => { path.style.cssText = ''; }, delay + 950);
    };
    const pulse = (path) => {
        const dot = document.createElementNS(NS, 'circle');
        dot.setAttribute('r', '3');
        dot.setAttribute('class', 'pulse');
        const motion = document.createElementNS(NS, 'animateMotion');
        motion.setAttribute('dur', '1.6s');
        motion.setAttribute('repeatCount', '1');
        motion.setAttribute('fill', 'freeze');
        motion.setAttribute('path', path.getAttribute('d'));
        dot.appendChild(motion);
        svg.appendChild(dot);
        motion.beginElement();
        setTimeout(() => dot.remove(), 1700);
    };

    if (reduced) {
        stage.classList.add('go');
        sats.forEach((s) => { s.dataset.state = 'online'; });
    } else {
        requestAnimationFrame(() => stage.classList.add('go'));
        sats.forEach((sat, i) => {
            const t = 400 + i * 260;
            drawIn(wires[i], t);
            setTimeout(() => { sat.dataset.state = 'deploying'; }, t + 1300);
            setTimeout(() => { sat.dataset.state = 'online'; }, t + 2100);
        });
        // Traffic: a dot travels down a random wire every so often; now and then a service redeploys.
        setTimeout(() => setInterval(() => {
            if (document.hidden) return;
            pulse(wires[Math.floor(Math.random() * wires.length)]);
        }, 700), 3800);
        setInterval(async () => {
            if (document.hidden) return;
            const sat = sats[Math.floor(Math.random() * sats.length)];
            if (sat.dataset.state !== 'online') return;
            sat.dataset.state = 'building';
            await wait(1500);
            sat.dataset.state = 'deploying';
            await wait(900);
            sat.dataset.state = 'online';
        }, 6500);
    }

    // Drag services like on the real canvas; wires follow.
    sats.forEach((sat) => {
        sat.addEventListener('pointerdown', (e) => {
            sat.setPointerCapture(e.pointerId);
            sat.classList.add('dragging');
            const s = stage.getBoundingClientRect();
            const [cx, cy] = center(sat);
            const dx = e.clientX - s.left - cx;
            const dy = e.clientY - s.top - cy;
            const move = (ev) => {
                const x = Math.min(Math.max(ev.clientX - s.left - dx, 0), s.width);
                const y = Math.min(Math.max(ev.clientY - s.top - dy, 0), s.height);
                sat.style.left = `${(x / s.width) * 100}%`;
                sat.style.top = `${(y / s.height) * 100}%`;
                layout();
            };
            const up = () => {
                sat.classList.remove('dragging');
                sat.removeEventListener('pointermove', move);
                sat.removeEventListener('pointerup', up);
                sat.removeEventListener('pointercancel', up);
            };
            sat.addEventListener('pointermove', move);
            sat.addEventListener('pointerup', up);
            sat.addEventListener('pointercancel', up);
        });
    });
})();

// Feature visuals: step through items with class "on", then mark the visual done; repeat.
const steps = async (viz, items, every, hold) => {
    const bar = viz.querySelector('.v-bar span');
    for (;;) {
        items.forEach((i) => i.classList.remove('on'));
        viz.classList.remove('done');
        if (bar) bar.style.width = '0';
        await wait(500);
        for (const [k, item] of items.entries()) {
            item.classList.add('on');
            if (bar) bar.style.width = `${((k + 1) / items.length) * 100}%`;
            await wait(every);
        }
        viz.classList.add('done');
        await wait(hold);
    }
};

document.querySelectorAll('.viz').forEach((viz) => {
    const items = [...viz.querySelectorAll('.v-steps li, .v-db, .v-checks li')];
    if (reduced) {
        items.forEach((i) => i.classList.add('on'));
        viz.classList.add('go', 'done');
        return;
    }
    onView(viz, () => {
        viz.classList.add('go');
        if (!items.length) return;
        if (viz.classList.contains('viz-network')) {
            // Let the domain finish typing before the checks run.
            setTimeout(() => steps(viz, items, 650, 4000), 1200);
        } else {
            steps(viz, items, 700, 3500);
        }
    });
});

// Deploy transcript: replay once with the real pauses compressed.
(() => {
    const term = document.querySelector('[data-term]');
    if (!term || reduced) return;
    const lines = [...term.querySelectorAll('code > span')];
    const kinds = lines.map((l) => l.className.replace('t-', ''));
    const delays = { cmd: 500, out: 160, phase: 420, dim: 110, ok: 520 };
    term.classList.add('playing');
    onView(term, () => {
        let t = 300;
        lines.forEach((line, i) => {
            t += delays[kinds[i]] ?? 150;
            setTimeout(() => line.classList.add('shown'), t);
        });
        setTimeout(() => term.classList.remove('playing'), t + 50);
    }, 0.6);
})();

// Screenshot tabs (arrow keys move between tabs).
const tabs = [...document.querySelectorAll('[role="tab"]')];
const select = (tab) => tabs.forEach((t) => {
    const on = t === tab;
    t.setAttribute('aria-selected', on);
    t.tabIndex = on ? 0 : -1;
    document.getElementById(t.getAttribute('aria-controls')).hidden = !on;
});
tabs.forEach((tab, i) => {
    tab.addEventListener('click', () => select(tab));
    tab.addEventListener('keydown', (e) => {
        const step = { ArrowRight: 1, ArrowLeft: -1 }[e.key];
        if (!step) return;
        const next = tabs[(i + step + tabs.length) % tabs.length];
        select(next);
        next.focus();
    });
});
