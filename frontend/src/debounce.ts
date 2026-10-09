/** Run `fn` once, `ms` after the last call to `call`.
 *
 *  The Sessions filters used to issue an audited listing per keystroke;
 *  this is the pause that turns typing "alice" into one read. `flush`
 *  runs it now (Enter), `cancel` drops a pending run (unmount). */
export function debounced(fn: () => void, ms: number) {
  let timer: ReturnType<typeof setTimeout> | undefined;
  const cancel = () => {
    if (timer !== undefined) clearTimeout(timer);
    timer = undefined;
  };
  return {
    call() {
      cancel();
      timer = setTimeout(() => {
        timer = undefined;
        fn();
      }, ms);
    },
    flush() {
      cancel();
      fn();
    },
    cancel,
  };
}
