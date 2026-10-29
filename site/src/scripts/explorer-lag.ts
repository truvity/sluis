// The index-lag example of the audit explorer: take the writer down and watch
// the transport queue grow while the lag stays where it is. All numbers are examples.
const root = document.querySelector<HTMLElement>('[data-ex-lag]');
const range = root?.querySelector<HTMLInputElement>('[data-lag-range]');
if (root && range) {
  const rate = Number(root.dataset.rate);
  const q = (s: string) => root.querySelector<HTMLElement>(s)!;
  const update = () => {
    const min = Number(range.value);
    q('[data-min-out]').textContent = String(min);
    q('[data-depth-out]').textContent = String(min * rate);
    q('[data-lag-msg]').textContent = min === 0
      ? 'The writer runs. The transport queue is empty and the lag is as in the table.'
      : `The writer has been down ${min} minutes: the leader sequence has not moved, so every lag reads as before, while ${min * rate} records wait in the transport queue. The trail is ${min} minutes behind the world.`;
  };
  range.addEventListener('input', update);
  update();
}
