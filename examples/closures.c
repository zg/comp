// Closures share the variables they capture, so inc and get see the same n.
let makeCounter = fn() {
  let n = 0;
  [fn() { n = n + 1; n }, fn() { n }]
};

let pair = makeCounter();
let inc = pair[0];
let get = pair[1];
inc(); inc(); inc();
puts("counter:", get());

let fib = fn(n) { if (n < 2) { n } else { fib(n - 1) + fib(n - 2) } };
puts("fib(25):", fib(25));

let map = fn(arr, f) {
  let out = []; let i = 0;
  while (i < len(arr)) { out = push(out, f(arr[i])); i = i + 1; }
  out
};
puts(map([1, 2, 3, 4, 5], fn(x) { x * x }));
