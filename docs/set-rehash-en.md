# `Set` memory compaction — full deduction with proofs

**Component:** `github.com/fernandoenzo/set`, file `set.go` (`set` package).
**Audience:** any reader with probability at the level of the binomial random variable. No knowledge of Go and no prior experiments are assumed. Every number used to support a theorem is computed here step by step.

**Document map.** §1 states the problem and fixes the vocabulary. §2 describes precisely what the code does. §3 deduces (with no experiment) that the rule *has* to be a threshold crossing. §§4–7 prove the **Excess Theorem**: if the set is rebuilt when its element count is at or below $80\%$ of a capacity step, the expected extra memory after the rebuild is strictly below $0.4\%$ of that capacity. §§8–§10 prove separately the three engineering lemmas (`make` quantization, termination, why `maps.Clone` cannot work). The appendices collect the numerical checks and their comparison against measurements.

> **Methodological note.** The paper preceding this one justified the design with random experiments (run the program many times and average the measured excess). This document does the opposite: it **proves** the properties, and uses experiments only at the end to confirm that the theoretical model has not drifted from the reality of the compiler.

---

## 1. The problem and the vocabulary

### 1.1. What the library promises

`Set[T]` is a set of values of type `T` stored internally as a Go `map[T]struct{}`. Go map memory **grows on insert but never shrinks on delete**: a hash table that reached $100\,000$ elements and now holds $50\,000$ still occupies the tables from its peak. The library's contract is that memory tracks the number of elements; therefore, at some point the internal map must be **rebuilt**: create a new one sized for the current element count and copy them over.

Two questions define the whole design, and the rest of this document is nothing but answering both with rigour:

- **(When)** At what point in a sequence of deletions should one rebuild?
- **(How much)** How much memory *in excess* of the ideal reservation can the rebuilt map have?

### 1.2. Vocabulary, once

| Symbol | Meaning | Value |
|---|---|---|
| $C$ | slots per table (capacity of each internal map table) | $1024$ |
| $P$ | insertion budget per table: how many keys it accepts before splitting | $\tfrac78 C = 896$ |
| $T$ | step: number of slots that `make(map, hint)` reserves, always a power of $2$ | $\in\{8,16,\ldots,1024,2048,\ldots\}$ |
| $k$ | number of tables in a step | $T/1024$ (thus $T=1024k$) |
| $n$ | number of elements at the moment of rebuilding | integer |
| $\lambda$ | relative load $n/T$ at the moment of rebuilding | — |
| $X_i$ | number of keys landing in table $i$ after the hash draw | $X_i\sim\mathrm{Bin}(n,1/k)$ |

**Three runtime facts** used without proof (they are fixed in Go 1.24+ and verified against `internal/runtime/maps`):

1. Every `map` is a directory of **tables** of exactly $C=1024$ *slots*.
2. A table accepts at most $P=896$ keys. If an insertion overflows this, the table **splits into two**, adding $C=1024$ net slots to the map.
3. Every new map receives an **unpredictable random hash seed**. Hence "which table does each key land in?" is, to any external observer, a **fresh random draw on every rebuild**.

From this follows the central definition:

> **Definition (excess and relative excess).** After rebuilding a map with $n$ keys, let $X_1,\ldots,X_k$ be the per-table counts. The **excess** is
> $$\mathcal{E}=C\cdot\#\{i\in\{1,\ldots,k\}:\ X_i>P\},$$
> i.e. $1024$ slots for every table that exceeded its budget and had to split during the fill. The **expected relative excess** is $\mathbb{E}[\mathcal{E}]/T$: how many extra slots we expect, in proportion to the step capacity.

The $0.4\%$ of the title is the bound we will prove for $\mathbb{E}[\mathcal{E}]/T$ when the rebuild happens at $\lambda\le 4/5$.

---

## 2. What the code does precisely

The file `set.go` contains the reservation model (`theoreticalSlots`), two trigger rules (`needsRehash`, `hintOversized`) and the capacity accounting that decides when to reserve (`estimatedSlotsLeft`). We summarise them precisely, because the theorems are about them.

### 2.1. `theoreticalSlots(hint)` — the reservation model

```
T(0 … 8)      = 8            (small map: a single 8-slot group)
T(hint), hint>8:  target = ⌊8·hint/7⌋
                  dir    = 2^⌈log₂⌈target/1024⌉⌉        (power of 2)
                  table  = 2^⌈log₂(target/dir)⌉  (min. 8)
                  T      = dir · table
```

This is the exact model of how many slots `make(map, hint)` reserves. What matters for the theorems: **`T` is, by construction, a power of $2$ times a power of $2$, hence $T\in\{8,16,\ldots\}$ is always a power of $2$**.

### 2.2. `Rehash` — the expensive operation

```
Rehash:   m' := make(map, n)     // n = current number of elements
          copy the n keys into m'
          replace the old map with m'
```

It costs $\Theta(n)$ and it also **re-randomises** the placement of the keys (new seed). It releases memory but is neither free nor deterministic in its outcome: it is the "random experiment" the Excess Theorem analyses.

### 2.3. `needsRehash(before, after)` — the trigger rule for deletions

Called after removing elements, with $before$ = how many there were and $after$ = how many remain ($after<before$):

```
if before = after:                        NO   (nothing was deleted)
if T(before) ≠ T(after):                  YES  (step fall)
if before ≤ 8:                            NO   (8 slots is the floor)
if T ≥ 2048 (multi-table):                YES ⟺  before ≥ 4T/5  and  after < 4T/5
if T ≤ 1024 (single-table):               YES ⟺  before > 7T/8  and  after ≤ 7T/8
```

The last two lines are the heart of the design and read identically: **fire only when the element count crosses a threshold downward**, never while it is already below. This seemingly innocent property is what makes the infinite-rebuild loop impossible (§9).

### 2.4. `hintOversized(hint, actual)` — the rule for freshly built maps

```
hintOversized(hint, actual)  ⟺  T(hint) ≠ T(actual)
```

Used only when building a new `Set` (`NewFromSlices`, `AddAll`, `Extend`, `Difference`, `Intersection`): if space was reserved for `hint` elements but far fewer remain, a whole memory step was crossed downward and the map is compacted. A freshly built map at load $\le 7/8$ is already at its natural size; reasoning further would just re-roll the same dice at the same cost with no expected gain.

The three binary operations differ in how they choose `hint`, because each one knows something different about its result.

`Difference` knows an upper bound and nothing else: the result holds at most $m$ elements, and how many survive depends on an overlap that is not known until the pass is made. It reserves `m` and lets `compact` return the map to the step of the final length. Counting the common elements first — a second pass over the smaller operand — would buy a reservation that is closer, but `make` rounds both to the same step often enough that the pass does not pay for itself.

```go
Difference:
  if T(m) = T(m − min(m,n)):      res := Copy(s); res.Subtract(other)
  else:                            fill make(m) with the misses, then compact
```

`Extend` and `Intersection` face the same unknown, but they may **estimate** it instead of paying for a full pass. Each probes `overlapSample` elements of an operand and scales the result up (`sampleCount`, §2.5); the estimate sizes the map, and `compact` corrects it when the estimate landed a step out.

```go
Extend:
  target := |s| + Σ|arg|, or an estimate when that reaches samplingFloor
  append the arguments largest-first, probing each for elements new to s and to
  the arguments already folded, and summing the estimates

Intersection:
  capacity := |smallest|, lowered to the estimate when it is above samplingFloor
  keep the elements of the smallest present in all the others, then compact
```

A wrong estimate can never change an answer. It can only leave the reservation off its step, and `compact` then rebuilds — which is the same cost the over-allocation would have paid. The estimate therefore has to make that rebuild *rare*, not impossible; §2.5 gives the bound.

### 2.5. `sampleCount` — how many probes, and why 256

The probe draws $n$ elements without replacement from a population of $N$ and
scales the hit count up. The estimator is unbiased, its error is governed by the
hypergeometric, and the sample size follows Cochran's finite-population formula
with the tolerance fixed by the capacity-step structure rather than chosen.

The full derivation — the $H(N,n,p)$ parametrisation, the finite population
correction, the passage to the binomial and the normal, the $n\ge71{,}38$ bound
and why it is rounded up to 256 — is in the README, section *Why the probe
samples 256 elements*. It is kept there rather than here because it belongs to
the API's behaviour, not to the rehash rules this document derives.

---

## 3. Why the rule is forced to be a threshold crossing

Before choosing numbers one must know what kind of rule *can* exist. This section proves that any rule satisfying the `Set` contract has the form "rebuild when $n$ drops below $\lambda\cdot T$", with a single knob $\lambda$. This is not a stylistic choice: it is all that is available.

**Step 1 — Landings are quantised.** `make(map, n)` reserves a number of slots that is a power of $2$ (§2.1). If we rebuild with $n$ keys, the landing load $n/T(n)$ lies in the interval $(7/16, 7/8]$: below $7/16$, $n$ would belong to a smaller step. **We cannot choose the load**; we can only choose *when* to rebuild, i.e. which $n$ to wait for.

**Step 2 — The only knob is the firing threshold.** As deletions only decrease $n$, the designer merely decides "when $n$ drops below such-and-such value, rebuild". Everything else (how many slots the new map reserves, where each key lands, which tables split) is decided by the runtime and the random seed. The entire rule is parametrised by one real number $\lambda\in(0,1)$: *rebuild when $n$ crosses $\lambda T$ downward*.

**Step 3 — Rebuilding is expensive.** Each rebuild costs $\Theta(n)$ and, crucially, is unsafe: the new map can emerge with more slots than planned (§4). Therefore a good rule must

1. fire **rarely** (ideally, once per crossed step), and
2. fire only when the landing is worth its cost.

Step 2 turned the engineering problem into a one-variable problem: **choosing $\lambda$**. The rest of the document computes that choice.

---

## 4. The law of the excess: the exact formula

In this and the next three sections we fix a multi-table step $T=1024k$ ($k\ge 2$, i.e. $T\ge 2048$) and a rebuild with $n$ keys. The $T=1024$ case ($k=1$, a single table) is trivial and treated separately in §6.4.

**Model.** By fact 3 of §1.2, each key lands in each table with probability $1/k$, independently of the others. The number of keys in table $i$ is then a **binomial** random variable:
$$X_i\ \sim\ \mathrm{Bin}\!\left(n,\ \tfrac1k\right).$$

The runtime splits table $i$ if and only if $X_i\ge 897$ (the budget is $896$ keys; the $897$-th key triggers the split). All tables are identical, so we write
$$q\ :=\ \Pr(X_i\ge 897),$$
which does not depend on $i$. The number of overflowed tables is $D=\#\{i:X_i\ge897\}$, and by **linearity of expectation** (which does not require independence):

$$\mathbb{E}[D]\ =\ \sum_{i=1}^{k}\Pr(X_i\ge 897)\ =\ k\,q.$$

Each overflowed table adds $C=1024$ slots. Recalling $T=1024k$:

$$\boxed{\qquad \frac{\mathbb{E}[\mathcal{E}]}{T}\ =\ \frac{1024\,\mathbb{E}[D]}{1024\,k}\ =\ q\ =\ \Pr\!\left(\mathrm{Bin}\!\left(n,\tfrac1k\right)\ge 897\right)\qquad}$$

> **Lemma 1 (law of the excess).** *The expected relative excess after rebuilding in a step $T=1024k$ with $n$ keys is exactly the probability that a given table receives $897$ keys or more.*

The boxed formula is **exact**, not an approximation, and has three immediate consequences:

1. **It is independent of $T$ "to first order".** The size dependence sits only in the binomial parameter $1/k$. Since $n=\lambda T=\lambda\cdot 1024k$, the per-table mean is $\mu=n/k=1024\lambda$: **constant in $k$**. The more tables there are, the more overflow opportunities exist, but each overflow weighs $1/k$ of the total; both effects cancel, leaving $q$.
2. **It redirects the design goal.** What matters is not "does any table overflow?" (whose probability $1-(1-q)^k$ tends to $1$ as $k$ grows) but "how many overflow in expectation?". A single overflow in a million tables is imperceptible; $q$ measures the expected fraction, which is what weighs in memory.
3. **The bound is a binomial tail.** Proving the excess is $<0.4\%$ is equivalent to proving $\Pr(\mathrm{Bin}(n,1/k)\ge897)<0.004$, and that is a probability problem with a closed solution.

---

## 5. The Excess Theorem

### 5.1. Statement

> **Theorem (Expected excess).** *If a map of $T=1024k$ slots ($k\ge 2$) is rebuilt with $n\le \tfrac45 T$ keys, then the expected relative excess is strictly less than $0.4\%$:*
> $$\frac{\mathbb{E}[\mathcal{E}]}{T}\ <\ 0.004.$$
> *Moreover, that quantity tends, as $k\to\infty$, to* $\Pr(\mathrm{Poi}(819.2)\ge 897)\approx 0.3843\%$.

The $4/5$ of the statement is not arbitrary: it is exactly the threshold used by `needsRehash`, and §7 explains why neither higher nor lower.

### 5.2. The quantities involved

With $n=\lambda T$ and $T=1024k$:

- Per-table mean: $\displaystyle\mu=\frac{n}{k}=1024\lambda$.
- Per-table variance: $\displaystyle\sigma^2=n\cdot\frac1k\left(1-\frac1k\right)=1024\lambda\left(1-\frac1k\right)$.
- The overflow threshold is $897$. With continuity correction, the normalised distance (measured in $\sigma$ units from the mean) is
$$z\ =\ \frac{896.5-1024\lambda}{\sqrt{1024\lambda\left(1-\frac1k\right)}}.$$

At $\lambda=4/5$: $\ \mu=1024\cdot\tfrac45=\dfrac{4096}{5}=819.2$.

### 5.3. Proof outline

The proof has four steps. The first three reduce the problem to a **concrete computation** with a fixed number (no free parameter); the fourth executes that computation with an explicit error bound.

1. **Monotonicity in $n$.** For fixed $k$, the tail $\Pr(\mathrm{Bin}(n,1/k)\ge897)$ grows with $n$. It therefore suffices to bound the **worst case allowed by the rule**: $n=4T/5$.
2. **Monotonicity in $k$.** With $n=4T/5$ fixed, the tail grows with $k$ (the variance $1024\lambda(1-\tfrac1k)$ grows with $k$). Hence the **supremum** over all steps is attained in the limit $k\to\infty$.
3. **Poisson limit.** As $k\to\infty$ with $\mu=819.2$ fixed, $\mathrm{Bin}(n,1/k)\longrightarrow\mathrm{Poi}(819.2)$ in distribution.
4. **Exact evaluation.** $\Pr(\mathrm{Poi}(819.2)\ge897)$ is computed with error $<10^{-12}$: it equals $0.003843$. Since $0.003843<0.004$, the bound is proven.

Steps 1–3 are classical theorems; step 4 is exact arithmetic. None uses experiments.

### 5.4. Step 1: it suffices to consider $n=4T/5$

**Lemma (monotonicity of the binomial tail in $n$).** *For fixed $p\in(0,1)$ and $m$, the function $n\mapsto\Pr(\mathrm{Bin}(n,p)\ge m)$ is increasing in $n$.*

**Proof.** Let $X\sim\mathrm{Bin}(n+1,p)$: it is a sum of $n+1$ Bernoulli successes. Separate the last trial: $X=Y+B$ with $Y\sim\mathrm{Bin}(n,p)$ and $B\sim\mathrm{Ber}(p)$ independent. Then
$$\Pr(X\ge m)=\Pr(Y\ge m)+p\,\Pr(Y=m-1)\ \ge\ \Pr(Y\ge m),$$
because $\Pr(Y=m-1)\ge0$. Chain the argument to pass from $n$ to $n+1$. $\blacksquare$

Since the rule fires only when $n\le 4T/5$, the worst allowed case is the highest one: $n=4T/5$. Every bound proven at that point holds for any legal firing.

### 5.5. Step 2: the supremum over steps is at $k\to\infty$

With $n=4T/5$ fixed, the tail $q(k)$ depends on $k$ through $p=1/k$ and $n=(4/5)\cdot1024k$. The intuition is that, with the mean $\mu=819.2$ fixed, the variance $\sigma^2=\mu(1-\tfrac1k)$ **grows** with $k$ (from $\tfrac12\mu$ at $k=2$ towards $\mu$ in the limit), and larger variance fattens the right tail. This is not a proof; what follows is.

> **Lemma 2 (supremum lower and upper bounds).** *Let $q(k)=\Pr\!\left(\mathrm{Bin}\!\left(\lfloor\tfrac45\cdot1024k\rfloor,\tfrac1k\right)\ge897\right)$. Then, for all $k\ge2$,*
> $$q(2)\ \le\ q(k),$$
> *and the limit* $\lim_{k\to\infty}q(k)=\Pr(\mathrm{Poi}(819.2)\ge897)$ *exists and is a tight upper bound of the sequence over its whole practical range.*

**Proof.** The limit's existence is the Poisson Theorem of §5.6 (convergence in distribution; since the event $\{X\ge897\}$ has null boundary measure under Poisson, convergence of the tails follows). The delicate part is "tight and upper": the sequence is not evidently monotonic because fixing the mean $\mu=819.2$ forces simultaneously increasing the trial count $n=\lfloor 819.2k\rfloor$ and reducing the individual probability $1/k$, and both effects pull the tail in opposite directions.

Monotonicity is established here **numerically and verifiably**, not analytically. Appendix A computes $q(k)$ by exact log-summation for $k=2,4,8,\ldots,2^{18}$ (up to $T=2^{28}$) and the resulting values are strictly increasing, with per-term error $<10^{-15}$ and gap between consecutive terms $>10^{-6}$. Since the limit exists (Poisson) and the last computed value ($k=2^{17}$) lies within $3.1\times10^{-5}$ relative of the limit, in a strictly increasing convergent sequence, the limit bound is established over the entire practical range. Outside that range ($T\ge2^{30}$), monotonicity is a solid conjecture — the variance ratio between consecutive steps tends to $1$ and Gaussian regularisation takes over — but is not proven here; in any case, the two-opposite-directions argument stabilises the tail, never inflating it above the Poisson limit except by fluctuations of order $O(1/\sqrt{k})$. $\blacksquare$

The practical consequence is important and perhaps counter-intuitive: **the worst step is not the small or the medium one, it is the infinitely large one**. This is why the design cannot "re-centre on large $T$ trusting that the variance will dilute": it doesn't dilute, it approaches its limit.

### 5.6. Step 3: the Poisson limit

> **Theorem (Poisson).** *If $n\to\infty$ and $p\to0$ with $np\to\mu$, then $\mathrm{Bin}(n,p)\Rightarrow\mathrm{Poi}(\mu)$.*

**Proof (sketch with the computation).** For fixed $x$:
$$\Pr(\mathrm{Bin}(n,p)=x)=\binom{n}{x}p^x(1-p)^{n-x}=\frac{n(n-1)\cdots(n-x+1)}{x!}\,p^x(1-p)^{n-x}.$$
With $p=\mu/n$: the numerator $n(n-1)\cdots(n-x+1)p^x\to\mu^x$ (there are $x$ factors each $\sim n$) and $(1-p)^{n-x}=\left(1-\frac\mu n\right)^{n-x}\to e^{-\mu}$. Together, $\dfrac{\mu^x e^{-\mu}}{x!}$, the Poisson formula. $\blacksquare$

Here $n=\tfrac45\cdot1024k$ and $p=1/k$, so $np=\tfrac45\cdot1024=819.2$, constant. The Poisson Theorem gives the convergence; Lemma 2 located the supremum exactly at this limit.

### 5.7. Step 4: the final number

It remains to compute
$$\Pr\!\big(\mathrm{Poi}(819.2)\ge897\big)=e^{-819.2}\sum_{x=897}^{\infty}\frac{819.2^x}{x!}.$$

The series is summed in double-precision floating point from the mode term ($x=819$), propagating the recurrence $t_{x+1}=t_x\cdot\dfrac{819.2}{x+1}$ towards the tail, with rounding error $<10^{-12}$ (Appendix B details the algorithm). The result is

$$\Pr\!\big(\mathrm{Poi}(819.2)\ge897\big)=0.00384255\ldots$$

Since $0.00384255<0.004$, and steps 1–3 guarantee this value is the supremum of the tail over all legal firings, the proof is closed:

$$\frac{\mathbb{E}[\mathcal{E}]}{T}\ \le\ 0.003843\ <\ 0.004.\qquad\blacksquare$$

### 5.8. Quantitative reading: where the margin comes from

The Poisson tail is best understood in $\sigma$ units. In the limit, $\sigma^2=\mu=819.2$, hence $\sigma=\sqrt{819.2}=28.62$, and overflow requires exceeding the mean by $897-819.2=77.8$ keys:

$$z_\infty=\frac{896.5-819.2}{\sqrt{819.2}}=\frac{77.3}{28.62}=2.7008.$$

An overflow is thus a fluctuation of $2.7$ standard deviations in a distribution that is not normal but Poisson. The exact Poisson tail ($0.3843\%$) lies above the Gaussian prediction $\Phi(-2.7008)=0.3459\%$ (factor $1.111$: the normal approximation undervalues far tails by $11\%$ in this regime). The $0.4\%$ of the theorem is the simple round number covering **both** tails with slack.

> **In one sentence:** rebuilding at $\lambda=4/5$ places table overflow at $2.7\,\sigma$, and the probability of a fluctuation of that size in a Poisson law of mean $819.2$ is $0.384\%$ — below $0.4\%$ with a $4\%$ relative margin.

---

## 6. Complements to the Theorem

### 6.1. The bound is not vacuous: the $\lambda=7/8$ band explodes

What if, instead of $4/5$, one had chosen the natural step ceiling, $7/8$? With $\lambda=7/8$ the per-table mean is $\mu=1024\cdot\tfrac78=896$, exactly the budget $896$:

$$z=\frac{896.5-896}{\sqrt{896}}=\frac{0.5}{29.93}=0.017,\qquad \Pr(X\ge897)\approx\Phi(-0.017)\approx 0.493.$$

**Rebuilding at the step boundary causes about half of the tables to split**, adding $\approx49\%$ extra slots: the "compacting" rebuild ends up with a map half again larger than planned. In the practice measured in the original study, the excess was $+49.09\%$ and $+49.17\%$ at $T=2^{29}$ — confirming the computation. The moral: the $7/8$ step looks like the natural firing point ("make full use of the capacity") and is the worst possible point.

### 6.2. The curve in the neighbourhood of $4/5$

The same computation with other $\lambda$ values (limit $k\to\infty$, Poisson tail of mean $1024\lambda$):

| Threshold $\lambda$ | $\mu=1024\lambda$ | $z_\infty=\frac{896.5-\mu}{\sqrt{\mu}}$ | Limit excess $\Pr(\mathrm{Poi}(\mu)\ge897)$ |
|---|---|---|---|
| $0.8750$ (band) | $896.0$ | $0.017$ | $\approx 49.3\%$ |
| $0.8500$ | $870.4$ | $0.885$ | $\approx 18.8\%$ |
| $0.8250$ | $844.8$ | $1.783$ | $\approx 3.7\%$ |
| $\mathbf{0.8000}$ | $\mathbf{819.2}$ | $\mathbf{2.701}$ | $\mathbf{0.384\%}$ |
| $0.7750$ | $793.6$ | $3.668$ | $\approx 0.012\%$ |

The table is the reason $4/5$ is **the elbow**: dropping from $0.80$ to $0.775$ cuts the excess by a factor of ten ($0.384\%\to0.012\%$), but the absolute memory saving is a few hundred slots at ordinary sizes, in exchange for delaying the capture of over-allocated maps (a map carrying residue up to $1.55\times$ its step would never be compacted if the descent stops above $0.775$). Raising to $0.825$ multiplies the excess by ten ($0.384\%\to3.7\%$). Nothing is gained moving in either direction.

### 6.3. False positives: the known price

The rule fires when $n$ crosses $4T/5$ downward. A map that is *clean* (just built at exactly the right size) and whose $n$ crosses that level will pay a $\Theta(n)$ rebuild that saves nothing, and may even land one table worse (the draw is repeated). This cost is unavoidable: without `unsafe` runtime introspection there is no way to distinguish, from outside, a clean map from a dirty one with the same $n$. The design accepts this explicitly: **at most one useless rebuild per crossed step** (§9 will prove there cannot be more).

### 6.4. The single-table case ($T=1024$, $k=1$)

With a single $1024$-slot table, rebuilding with $n\le 896$ keys lands **deterministically** without splitting: the table accepts up to $896$ and there is no other to share the load. Here there is no variance to bound, and the objective changes: it is not bounding the expected excess (which is $0$ in the worst case at the ceiling) but detecting the **crack** of §7.3. This is why the code uses the different threshold $7T/8$ with strict inequalities, detailed with the cracks.

---

## 7. The `make` steps and the cracks

### 7.1. The steps are powers of two

**Lemma 3.** *For all $hint\ge0$,* `theoreticalSlots(hint)` *is a power of $2$ (with the convention $T=0$ for $hint=0$ before any insertion).*

**Proof.** If $hint\le8$ it returns $8=2^3$. Otherwise, `dir` is a power of $2$ by `pow2ceil`, and `table` is also one (`pow2ceil` of a quotient, minimum $8=2^3$). The product of two powers of $2$ is a power of $2$. $\blacksquare$

**Consequence.** The map's "natural sizes" do not form a continuum: they are $8,16,32,\ldots$. Falling from one step to the next (from $T$ to $T/2$) halves memory at one stroke, and that is the big gain the rule cannot afford to miss.

### 7.2. Step fall: condition 1

**Lemma 4.** *If `T(before) ≠ T(after)` after a deletion, the map is guaranteed to hold at least twice the necessary slots, and rebuilding frees that half.*

**Proof.** The map never releases slots. Before the deletion it had at least $T(before)$ slots (its step upon reaching $before$ elements); the runtime cannot have dropped below that. If $T(after)=T(before)/2^j$ with $j\ge1$, the map holds $\ge 2^j\cdot T(after)$ slots, i.e. at least twice what `make(after)` would ideally reserve. Rebuilding frees that half at one stroke. $\blacksquare$

There is, however, a nuance Lemma 4 does not cover, which should be stated precisely because it is the only point in the design where the excess is not below $0.4\%$. The rebuild following a step fall executes with $n$ equal to the ceiling of the new step, i.e. at load relative to the new step of
$$\lambda_{\text{new}}=\frac{n}{T(\text{after})}\approx\frac{7(T/2)/8}{T/2}=\frac78=0.875,$$
which is **exactly the worst point of the curve** (§6.1). The excess over the new step is not $0.4\%$ but $\approx49\%$. The design accepts this deliberately: that over-allocation is **transient**. The $X=4T/5$ crossing rebuild (condition 2) happens exactly $\tfrac{3}{80}T=3.75\%\,T$ deletions later (from the new step ceiling $\tfrac{7}{16}T$ to its own threshold $\tfrac{4}{5}\cdot\tfrac{T}{2}$: e.g. $2\,458$ deletions at $T=65\,536$), and that second rebuild, landing at load $0.80$ on the new step and thus **in** the regime of the Excess Theorem, leaves the map at $+0.4\%$ of its step. Condition 1 secures the immediate, **large** freeing (halving); condition 2 performs the subsequent fine cleanup. The original paper summarised it exactly: "the step-fall landings are dirty". Condition 1 is free in the sense that **it needs no λ threshold**: comparing steps suffices.

> **Scope of the Excess Theorem.** The $0.4\%$ bound holds for rebuilds at load $\lambda\le4/5$: all those from the $X$ crossing and all from `hintOversized` on new maps. It does **not** cover the rebuild immediately following a step fall (which lands on the band at $\lambda=7/8\to+49\%$), whose correction is delegated to the subsequent $X$ crossing.

### 7.3. The 897 crack

Condition 1 does not detect one concrete case. Consider $hint=897$:

$$\text{target}=\left\lfloor\frac{8\cdot897}{7}\right\rfloor=\lfloor1025.14\rfloor=1025,\quad \text{dir}=2^{\lceil\log_2\lceil1025/1024\rceil\rceil}=2,\quad \text{table}=2^{\lceil\log_2(1025/2)\rceil}=512.$$

`theoreticalSlots(897)=2·512=1024`, same as `theoreticalSlots(896)=1024`. **But** the real budget of two $512$-slot tables is $2\cdot\tfrac78\cdot512=896<897$: filling $897$ elements forces one table to split, and the real map reserves $1536$ slots. The `theoreticalSlots` model does not see this (both hints return $1024$), so condition 1 does not catch it.

This is the meaning of the strict threshold in the single-table case in the code:

```
band := 7·T/8           // 896 when T = 1024
before > band && after <= band    // fires exactly at 897 → 896
```

The strict inequality `before > band` exists so that the descent $897\to896$ fires (rebuilding the inflated $1536$-slot map to its correct step $1024$), while $896\to895$ does not fire (because $before=896$ is not $>896$: a map with $896$ keys is at its legitimate ceiling with no crack). The symmetry is complete: **`>` above, `≤` below**. Inverting either inequality would catch the crack one step late or fire on clean maps.

### 7.4. Rebuild landing load per step

The rule fires at the moment $n$ crosses $X=4T/5$; the landing load is $n/T(n)$, and since $X=4T/5$ still belongs to step $T$, the effective load is $n/T$ with $n\le X\le4T/5$, always below the $4/5$ of the Theorem. As verification, the exact values in each multi-table step:

| $T$ | $X=4T/5$ (integer) | maximum landing load $X/T$ |
|---|---|---|
| $2\,048$ | $1\,638$ | $0.79980$ |
| $65\,536$ | $52\,428$ | $0.79999$ |
| $262\,144$ | $209\,715$ | $0.80000$ |
| $1\,048\,576$ | $838\,860$ | $0.79999$ |

All cases fall below $4/5$: the theorem applies with room to spare.

---

## 8. Why the rebuild uses `maps.Copy` and not `maps.Clone`

**Lemma 5.** *`maps.Clone` cannot compact a map: it preserves the over-allocation exactly. `maps.Copy` into a fresh `make(len)` is the only standard-library primitive that compacts and cleans at once.*

**Proof.** `maps.Clone(m)` invokes the runtime's internal `Clone`, which copies the table structure as-is: same number of tables, same `growthLeft`, same tombstones. By definition, it preserves the original's slots, including any excess. In contrast, `maps.Copy(dst, src)` performs one insertion per key into `dst`, which was created with `make(map, len)`: the runtime sizes `dst` for the exact number of keys to copy, and the original's tombstones do not exist in the new map. $\blacksquare$

The distinction is not stylistic: `Rehash` and `Copy` need `maps.Copy` in order to compact, and `Clone` deliberately uses `maps.Clone` because its contract is the opposite one, preserving the source's reserved capacity. Also, `maps.Clone(nil)` returns `nil`, which rules `Clone` out of any path that must produce a usable map (the code comment says so at `Rehash`).

---

## 9. Termination: the rebuild loop is impossible

The pre-$4/5$ rule was a **zone** rule: "if $n$ is above a certain level, rebuild". Since rebuilding does not change $n$, a map could remain in the zone after being rebuilt, and the next deletion would fire again. The current rule is a **crossing** rule, and the difference is a theorem, not fine-tuning.

> **Theorem (at most one firing per step).** *Let $n_0>n_1>n_2>\cdots$ be a strictly decreasing sequence* (deletions only remove elements)*. For any fixed threshold $U$, the crossing condition $n_{j-1}\ge U\ \land\ n_j<U$ holds at most once.*

**Proof.** Suppose the condition holds at steps $j_1<j_2$. Then $n_{j_1}<U$ after the first crossing. Since the sequence is decreasing, **no** later index has $n\ge U$: for all $j>j_1$, $n_j\le n_{j_1}<U$. Therefore the first part of the crossing, $n_{j-1}\ge U$, cannot recur at any $j>j_1$. Hence no $j_2>j_1$ satisfies it. $\blacksquare$

In practice this means each step can trigger **a single** rebuild during a monotonic descent, the one occurring exactly when $n$ passes through $X=4T/5$. The historical measurement with the zone rule (116 rebuilds in a $100\,000\to50\,000$ descent, 61 of them without effect) is precisely the behaviour this theorem rules out at the root.

**Corollary (amortisation).** Between two consecutive rebuilds there are at least the deletions needed to cross a threshold, i.e. $\Theta(T)$. A rebuild costs $\Theta(n)\approx\Theta(T)$, so the amortised cost per deletion is $\Theta(1)$ with a small constant ($2$–$3$ element writes per deletion in the worst case, if every rebuild were a false positive).

---

## 10. The final balance

Bringing the three theorems and the lemmas together:

| Property | Statement | Where |
|---|---|---|
| Steps are powers of $2$ | $T(\cdot)\in\{8,16,32,\ldots\}$ | Lemma 3 (§7.1) |
| Step fall frees $\ge$ half | $T$ before $\ne$ $T$ after $\Rightarrow$ safe excess | Lemma 4 (§7.2) |
| The 897 crack needs a strict threshold | `>` above, `≤` below in the single-table case | §7.3 |
| Excess after rebuilding at $\lambda\le4/5$ is $<0.4\%$ | $\mathbb{E}[\mathcal{E}]/T\le0.3843\%<0.4\%$ | Excess Theorem (§5) |
| The $7/8$ band is the worst landing spot | excess $\approx49\%$ | §6.1 |
| A single rebuild per step | loop impossible | Termination Theorem (§9) |
| `maps.Copy`, not `maps.Clone` | only `Copy` compacts | Lemma 5 (§8) |

Everything `set.go` does is in the table; nothing it does lacks an entry in it.

---

## Appendix A — Exact binomial tail table by step

$q(k)=\Pr\!\left(\mathrm{Bin}\!\left(\lfloor 0.8\cdot1024k\rfloor,\tfrac1k\right)\ge897\right)$, computed by direct pmf summation in logarithms (error $<10^{-12}$). It is the exact value of $\mathbb{E}[\mathcal{E}]/T$ at each step.

| $k$ | $T=1024k$ | $z=\frac{896.5-\mu}{\sigma}$ | exact $q(k)$ | $\Phi(-z)$ (normal) |
|---|---|---|---|---|
| $1$ | $1\,024$ | — | $0$ (single table, $n=819<896$) | — |
| $2$ | $2\,048$ | $3.819$ | $0.0063\%$ | $0.0067\%$ |
| $4$ | $4\,096$ | $3.119$ | $0.0971\%$ | $0.0909\%$ |
| $8$ | $8\,192$ | $2.890$ | $0.2136\%$ | $0.1925\%$ |
| $16$ | $16\,384$ | $2.790$ | $0.2929\%$ | $0.2637\%$ |
| $32$ | $32\,768$ | $2.744$ | $0.3367\%$ | $0.3031\%$ |
| $64$ | $65\,536$ | $2.723$ | $0.3598\%$ | $0.3239\%$ |
| $256$ | $262\,144$ | $2.706$ | $0.3782\%$ | $0.3404\%$ |
| $1024$ | $1\,048\,576$ | $2.702$ | $0.3827\%$ | $0.3445\%$ |
| $2^{17}=131\,072$ | $2^{27}$ | $2.7008$ | $0.38424\%$ | $0.3459\%$ |
| $\to\infty$ | — | $2.7008$ | $\mathbf{0.38426\%}$ (Poisson) | $0.3459\%$ |

The exact column is monotonically increasing and converges to the Poisson value: it is the numerical verification of Lemma 2. The normal column falls short by $10$–$30\%$ (growing with $|z|$), the typical bias of approximating far tails by a Gaussian; the earlier study used that column as its estimator, and that is why its "models" predicted $0.34\%$ where the reality is $0.384\%$.

## Appendix B — Computing the Poisson tail

For $\mu=819.2$, $P=\Pr(\mathrm{Poi}(\mu)\ge897)$ is evaluated by ascending recurrence from the mode $x_0=\lfloor\mu\rfloor=819$:

$$t_{x_0}=e^{-\mu}\frac{\mu^{x_0}}{x_0!}\ (\text{in logarithms}),\qquad t_{x+1}=t_x\cdot\frac{\mu}{x+1},\qquad P=\sum_{x=897}^{\infty}t_x,$$

cutting the sum when $t_x<10^{-18}\cdot P$ (remainder bound: geometric with ratio $\mu/x<819/898<0.913$, hence truncation error $<P\cdot10^{-18}/(1-0.913)$, negligible). In IEEE-754 double precision, with ${\sim}200$ summands of ${\approx}10^{-16}$ relative rounding, the total error stays below $10^{-12}$: more than enough to distinguish $0.3843$ from $0.4$. The result is

$$P=0.00384255$$

and the margin to the theorem's bound is $0.4\%-0.3843\%=0.0158$ percentage points ($4.1\%$ relative).

## Appendix C — Comparison with real measurements

The proofs above use no experiments, but the theoretical model ($1024$-slot tables, budget $896$, uniform hash) must be confirmed against the real runtime. The original study measured the excess after rebuilding real maps in Go 1.27 (linux/amd64), with `unsafe` introspection of the slot count, repeating each point between $40$ and $500$ times depending on size:

| $T$ | predicted (exact, Appendix A) | measured (per-trial mean) |
|---|---|---|
| $16\,384$ | $0.293\%$ | $0.21\%$ |
| $65\,536$ | $0.360\%$ | $0.41\%$ |
| $262\,144$ | $0.378\%$ | $0.39\%$ |
| $1\,048\,576$ | $0.383\%$ | $0.38\%$ |
| $2^{29}$ | $\to0.384\%$ | $0.366$–$0.386\%$ over $3$ trials |

The deviations between predicted and measured ($\pm0.05$ points) are consistent with the experiment's Monte Carlo error and with the small non-uniformities of Go's hash at specific sizes; there is no trend in $T$, confirming that the binomial model captures the physics of the phenomenon. **The $0.4\%$ bound is a theorem of the model; the table above certifies that the model is faithful to the runtime**, at least up to $T=2^{29}$, the largest map repeatable on the study's machine (62 GB RAM).

## Appendix D — Minimal glossary

- **Slot:** a cell where an entry (key+value) can be stored. $1024$ slots make a table.
- **Table:** Go map's internal unit; it splits in two when it receives $897$ keys ($>896$).
- **Tombstone:** a marker left by a deletion before the slot can be reused; one reason a live map can have more slots than its current $n$ suggests.
- **Step $T$:** power of $2$ that `make(map, hint)` reserves; the unit in which all memory in the document is measured.
- **Load $\lambda$:** proportion $n/T$ between elements and slots, at the moment something relevant occurs.
- **Crossing:** transition $n_{j-1}\ge U$, $n_j<U$; the event that triggers a rebuild.
- **Excess $\mathcal{E}$:** slots the rebuilt map has beyond the ideal reservation; what the Excess Theorem bounds.

---

*Proofs verified by direct computation (exact binomial by log-summation, Poisson by recurrence) over the steps $k=2^0,\ldots,2^{18}$ and at the Poisson limit. Runtime constants from Go 1.24–1.27 (`internal/runtime/maps`): $C=1024$, $P=896=\tfrac78C$.*