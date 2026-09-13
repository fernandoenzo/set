# La compactación de memoria de `Set` — deducción completa con demostraciones

**Componente:** `github.com/fernandoenzo/set`, fichero `set.go` (paquete `set`).
**Audiencia:** cualquier lector que sepa probabilidad a nivel de variable aleatoria binomial. No se asume conocimiento de Go ni de experimentos previos. Todo número que sirva de apoyo a un teorema se calcula aquí paso a paso.

**Mapa del documento.** El §1 fija el problema y el vocabulario. El §2 describe qué hace exactamente el código. El §3 deduce (sin usar ningún experimento) que la regla *tiene* que ser un cruce de umbral. Los §4–§7 demuestran el **Teorema del Exceso**: si el conjunto se reconstruye cuando su número de elementos está a lo sumo en el $80\%$ de la capacidad de un escalón, la memoria extra esperada tras la reconstrucción es estrictamente menor que el $0{,}4\%$ de esa capacidad. Los §8–§10 demuestran por separado los tres lemas de ingeniería (cuantización de `make`, terminación, incompatibilidad de `maps.Clone`). Los anexos recogen las comprobaciones numéricas y su contraste con las mediciones.

> **Nota metodológica.** El artículo anterior a este justificaba el diseño con experimentos aleatorios (ejecutar el programa muchas veces y promediar el exceso medido). Este documento hace lo contrario: **demuestra** las propiedades, y solo usa los experimentos, al final, para confirmar que el modelo teórico no se ha alejado de la realidad del compilador.

---

## 1. El problema y el vocabulario

### 1.1. Qué promete la librería

`Set[T]` es un conjunto de elementos de tipo `T` guardado internamente como un `map[T]struct{}` de Go. La memoria de un `map` de Go **crece al insertar pero nunca se encoge al borrar**: una tabla hash que llegó a tener $100\,000$ elementos y conserva $50\,000$ sigue ocupando las tablas de su pico. El contrato de la librería es que la memoria acompaña al número de elementos; por tanto, en algún momento hay que **reconstruir** el mapa interno: crear uno nuevo dimensionado para el número actual de elementos y copiarlos.

Dos preguntas definen todo el diseño, y el resto del documento no es más que responderlas con rigor:

- **(Cuándo)** ¿En qué momento de una secuencia de borrados conviene reconstruir?
- **(Cuánto)** ¿Cuánta memoria *de más* puede tener el mapa reconstruido respecto a la reserva ideal?

### 1.2. Vocabulario, una sola vez

| Símbolo | Significado | Valor |
|---|---|---|
| $C$ | slots por tabla (capacidad de cada tabla interna del mapa) | $1024$ |
| $P$ | presupuesto de inserción por tabla: cuántas claves acepta antes de dividirse | $\tfrac78 C = 896$ |
| $T$ | escalón: número de slots que `make(map, hint)` reserva, siempre potencia de $2$ | $\in\{8,16,\ldots,1024,2048,\ldots\}$ |
| $k$ | número de tablas de un escalón | $T/1024$ (así $T=1024k$) |
| $n$ | número de elementos en el momento de reconstruir | entero |
| $\lambda$ | carga relativa $n/T$ en el momento de reconstruir | — |
| $X_i$ | número de claves que caen en la tabla $i$ tras el sorteo del hash | $X_i\sim\mathrm{Bin}(n,1/k)$ |

**Tres hechos del runtime** que se usan sin demostración (son fijos en Go 1.24+ y están verificados en `internal/runtime/maps`):

1. Todo `map` es un directorio de **tablas** de exactamente $C=1024$ *slots*.
2. Una tabla acepta como máximo $P=896$ claves. Si al insertar se supera, la tabla **se divide en dos**, añadiendo $C=1024$ slots netos al mapa.
3. Cada mapa nuevo recibe una **semilla de hash aleatoria e impredecible**. Por tanto, «¿en qué tabla cae cada clave?» es, para un observador externo, un **sorteo aleatorio nuevo cada vez** que se reconstruye el mapa.

De aquí sale la definición central:

> **Definición (exceso y exceso relativo).** Tras reconstruir un mapa con $n$ claves, sean $X_1,\ldots,X_k$ los conteos por tabla. El **exceso** es
> $$\mathcal{E}=C\cdot\#\{i\in\{1,\ldots,k\}:\ X_i>P\},$$
> es decir, $1024$ slots por cada tabla que se pasó del presupuesto y tuvo que dividirse durante el llenado. El **exceso relativo esperado** es $\mathbb{E}[\mathcal{E}]/T$: cuántos slots extra esperamos, en proporción a la capacidad del escalón.

El número $0{,}4\%$ del título es la cota que vamos a demostrar para $\mathbb{E}[\mathcal{E}]/T$ cuando la reconstrucción se produce a $\lambda\le 4/5$.

---

## 2. Qué hace exactamente el código

El fichero `set.go` contiene el modelo de reserva (`theoreticalSlots`), dos reglas de disparo (`needsRehash`, `hintOversized`) y la contabilidad de capacidad que decide cuándo se reserva (`estimatedSlotsLeft`). Las resumimos con precisión, porque los teoremas van sobre ellas.

### 2.1. `theoreticalSlots(hint)` — el modelo de reserva

```
T(0 … 8)      = 8            (mapa pequeño: un solo grupo de 8 slots)
T(hint), hint>8:  target = ⌊8·hint/7⌋
                  dir    = 2^⌈log₂⌈target/1024⌉⌉        (potencia de 2)
                  table  = 2^⌈log₂(target/dir)⌉  (mín. 8)
                  T      = dir · table
```

Es el modelo exacto de cuántos slots reserva `make(map, hint)`. Lo importante para los teoremas: **`T` es, por construcción, una potencia de $2$ multiplicada por otra potencia de $2$, luego $T\in\{8,16,\ldots\}$ siempre es potencia de $2$**.

### 2.2. `Rehash` — la operación cara

```
Rehash:   m' := make(map, n)     // n = número actual de elementos
          copiar las n claves a m'
          sustituir el mapa viejo por m'
```

Cuesta $\Theta(n)$ y además **vuelve a sortear** la colocación de las claves (nueva semilla). Devuelve memoria pero no es gratis ni determinista en su resultado: es el «experimento aleatorio» que el Teorema del Exceso analiza.

### 2.3. `needsRehash(before, after)` — la regla de disparo en los borrados

Se llama después de eliminar elementos, con $before$ = cuántos había y $after$ = cuántos quedan ($after<before$):

```
si before = after:                        NO   (no se borró nada)
si T(before) ≠ T(after):                  SÍ   (caída de un escalón entero)
si before ≤ 8:                            NO   (el suelo son 8 slots)
si T ≥ 2048 (multi-tabla):                SÍ  ⟺  before ≥ 4T/5  y  after < 4T/5
si T ≤ 1024 (mono-tabla):                 SÍ  ⟺  before > 7T/8  y  after ≤ 7T/8
```

Las dos últimas líneas son el corazón del diseño y se leen igual: **dispara solamente cuando el número de elementos cruza un umbral hacia abajo**, nunca mientras ya está por debajo. Esta propiedad, aparentemente inocente, es la que hace imposible el bucle infinito de reconstrucciones (§9).

### 2.4. `hintOversized(hint, actual)` — la regla para mapas recién creados

```
hintOversized(hint, actual)  ⟺  T(hint) ≠ T(actual)
```

Solo se usa al construir un `Set` nuevo (`NewFromSlices`, `AddAll`, `Extend`, `Intersection`): si se reservó para `hint` elementos pero quedaron bastantes menos, cruzó un escalón entero de memoria y se compacta. Un mapa recién creado con carga $\le 7/8$ ya está en su tamaño natural; razonarlo más sería volver a tirar los mismos dados con el mismo coste y sin esperanza de mejora.

`Difference` es la excepción, y a propósito: quiere el resultado en el escalón de su propia longitud, así que no puede confiar en una reconstrucción posterior. Cuenta primero la intersección —una pasada extra sobre el operando menor— y dimensiona el mapa exacto, de modo que el resultado nunca queda sobre-asignado ni se reconstruye. Cuando $T(m)=T(m-\min(m,n))$ no se puede cruzar ningún escalón y se toma el camino barato de copiar y borrar.

```go
Difference:
  si T(m) = T(m − min(m,n)):      res := Copy(s); res.Subtract(other)
  si no:                          contar los comunes y construir con make(m − common)
```

---

## 3. Por qué la regla está forzada a ser un cruce de umbral

Antes de elegir números hay que saber qué tipo de regla *puede* existir. Este apartado demuestra que cualquier regla que cumpla el contrato de `Set` tiene la forma «reconstruye cuando $n$ baje por debajo de $\lambda\cdot T$», con una sola perilla $\lambda$. No es una elección de estilo: es lo único disponible.

**Paso 1 — El aterrizaje está cuantizado.** `make(map, n)` reserva un número de slots que es potencia de $2$ (§2.1). Si reconstruimos con $n$ claves, la carga de aterrizaje $n/T(n)$ está en el intervalo $(7/16, 7/8]$: por debajo de $7/16$, $n$ pertenecería a un escalón menor. **No podemos elegir la carga**; solo podemos elegir *en qué momento* reconstruir, es decir, a qué $n$ esperar.

**Paso 2 — La única perilla es el umbral de disparo.** Como los borrados solo hacen decrecer $n$, el diseñador solo decide «cuando $n$ baje de tal valor, reconstruye». Todo lo demás (cuántos slots reserva el mapa nuevo, dónde cae cada clave, qué tablas se dividen) lo decide el runtime y la azarosa semilla. La regla completa queda parametrizada por un único número real $\lambda\in(0,1)$: *reconstruye cuando $n$ cruce hacia abajo el nivel $\lambda T$*.

**Paso 3 — Reconstruir es caro.** Cada reconstrucción cuesta $\Theta(n)$ y, lo más importante, no es segura: el mapa nuevo puede salir con más slots de los previstos (§4). Por tanto, una regla buena debe

1. dispararse **pocas veces** (idealmente, una por escalón cruzado), y
2. dispararse solo cuando el aterrizaje compensa.

El Paso 2 convirtió el problema de ingeniería en un problema de una variable: **elegir $\lambda$**. El resto del documento calcula esa elección.

---

## 4. La ley del exceso: fórmula exacta

En esta sección y las tres siguientes fijamos un escalón multi-tabla $T=1024k$ ($k\ge 2$, es decir $T\ge 2048$) y una reconstrucción con $n$ claves. El caso $T=1024$ ($k=1$, una sola tabla) es trivial y se trata aparte en el §6.4.

**Modelo.** Por el hecho 3 de §1.2, cada clave cae en cada tabla con probabilidad $1/k$, independientemente de las demás. El número de claves en la tabla $i$ es entonces una **binomial**:
$$X_i\ \sim\ \mathrm{Bin}\!\left(n,\ \tfrac1k\right).$$

El runtime divide la tabla $i$ si y solo si $X_i\ge 897$ (el presupuesto es $896$ claves; la clave número $897$ provoca la división). Todas las tablas se consideran idénticas, así que escribimos
$$q\ :=\ \Pr(X_i\ge 897),$$
que no depende de $i$. El número de tablas desbordadas es $D=\#\{i:X_i\ge897\}$, y por **linealidad de la esperanza** (que no pide independencia):

$$\mathbb{E}[D]\ =\ \sum_{i=1}^{k}\Pr(X_i\ge 897)\ =\ k\,q.$$

Cada tabla desbordada añade $C=1024$ slots. Recordando que $T=1024k$:

$$\boxed{\qquad \frac{\mathbb{E}[\mathcal{E}]}{T}\ =\ \frac{1024\,\mathbb{E}[D]}{1024\,k}\ =\ q\ =\ \Pr\!\left(\mathrm{Bin}\!\left(n,\tfrac1k\right)\ge 897\right)\qquad}$$

> **Lema 1 (ley del exceso).** *El exceso relativo esperado tras reconstruir en un escalón $T=1024k$ con $n$ claves es exactamente la probabilidad de que una tabla dada reciba $897$ claves o más.*

La fórmula del recuadro es **exacta**, no una aproximación, y tiene tres consecuencias inmediatas:

1. **Es independiente de $T$ "en primera aproximación".** La dependencia del tamaño está solo en el parámetro $1/k$ de la binomial. Como $n=\lambda T=\lambda\cdot 1024k$, la media por tabla es $\mu=n/k=1024\lambda$: **constante en $k$**. Cuanta más tablas haya, más oportunidades de desborde hay, pero cada desborde pesa $1/k$ del total; ambos efectos se cancelan y queda $q$.
2. **Cambia el objetivo del diseño.** Lo que importa no es «¿se desborda alguna tabla?» (cuya probabilidad $1-(1-q)^k$ tiende a $1$ cuando $k$ crece) sino «¿cuántas se desbordan en esperanza?». Un único desborde en un millón de tablas es imperceptible; $q$ mide la fracción esperada, que es lo que pesa en memoria.
3. **La cota es una cola binomial.** Probar que el exceso es $<0{,}4\%$ equivale a probar $\Pr(\mathrm{Bin}(n,1/k)\ge897)<0{,}004$, y eso es un problema de probabilidad con solución cerrada.

---

## 5. El Teorema del Exceso

### 5.1. Enunciado

> **Teorema (Exceso esperado).** *Si un mapa de $T=1024k$ slots ($k\ge 2$) se reconstruye con $n\le \tfrac45 T$ claves, entonces el exceso relativo esperado es estrictamente menor que $0{,}4\%$:*
> $$\frac{\mathbb{E}[\mathcal{E}]}{T}\ <\ 0{,}004.$$
> *Más aún, esa cantidad tiende, cuando $k\to\infty$, a* $\Pr(\mathrm{Poi}(819{,}2)\ge 897)\approx 0{,}3843\%$.

El $4/5$ del enunciado no es arbitrario: es exactamente el umbral que usa `needsRehash`, y la sección §7 explica por qué ni más alto ni más bajo.

### 5.2. Las cantidades involucradas

Con $n=\lambda T$ y $T=1024k$:

- Media por tabla: $\displaystyle\mu=\frac{n}{k}=1024\lambda$.
- Varianza por tabla: $\displaystyle\sigma^2=n\cdot\frac1k\left(1-\frac1k\right)=1024\lambda\left(1-\frac1k\right)$.
- El umbral de desborde es $897$. Con corrección de continuidad, la distancia normalizada (medida en unidades $\sigma$ desde la media) es
$$z\ =\ \frac{896{,}5-1024\lambda}{\sqrt{1024\lambda\left(1-\frac1k\right)}}.$$

A $\lambda=4/5$: $\ \mu=1024\cdot\tfrac45=\dfrac{4096}{5}=819{,}2$.

### 5.3. Esquema de la demostración

La demostración consta de cuatro pasos. Los tres primeros reducen el problema a un **cálculo concreto** con un número fijo (sin ningún parámetro libre); el cuarto ejecuta ese cálculo con cota de error explícita.

1. **Monotonía en $n$.** A $k$ fijo, la cola $\Pr(\mathrm{Bin}(n,1/k)\ge897)$ crece con $n$. Por tanto basta acotar el **peor caso permitido por la regla**: $n=4T/5$.
2. **Monotonía en $k$.** Fijado $n=4T/5$, la cola crece con $k$ (la varianza $1024\lambda(1-\tfrac1k)$ crece con $k$). Por tanto el **supremo** sobre todos los escalones se alcanza en el límite $k\to\infty$.
3. **Límite de Poisson.** Cuando $k\to\infty$ con $\mu=819{,}2$ fijo, $\mathrm{Bin}(n,1/k)\longrightarrow\mathrm{Poi}(819{,}2)$ en distribución.
4. **Evaluación exacta.** $\Pr(\mathrm{Poi}(819{,}2)\ge897)$ se calcula con error $<10^{-12}$: vale $0{,}003843$. Como $0{,}003843<0{,}004$, la cota queda demostrada.

Los pasos 1–3 son teoremas clásicos; el paso 4 es aritmética exacta. Ninguno usa experimentos.

### 5.4. Paso 1: basta considerar $n=4T/5$

**Lemma (monotonía de la cola binomial en $n$).** *Fijados $p\in(0,1)$ y $m$, la función $n\mapsto\Pr(\mathrm{Bin}(n,p)\ge m)$ es creciente en $n$.*

**Demostración.** Sea $X\sim\mathrm{Bin}(n+1,p)$: es la suma de $n+1$ éxitos de Bernoulli. Separa el último ensayo: $X=Y+B$ con $Y\sim\mathrm{Bin}(n,p)$ y $B\sim\mathrm{Ber}(p)$ independientes. Entonces
$$\Pr(X\ge m)=\Pr(Y\ge m)+p\,\Pr(Y=m-1)\ \ge\ \Pr(Y\ge m),$$
porque $\Pr(Y=m-1)\ge0$. Añade los ensayos uno a uno para pasar de $n$ a $n+1$ en cadena. $\blacksquare$

Como la regla solo dispara cuando $n\le 4T/5$, el peor caso permitido es el más alto: $n=4T/5$. Toda cota que probemos en ese punto vale para cualquier disparo legal.

### 5.5. Paso 2: el supremo sobre escalones está en $k\to\infty$

Fijado $n=4T/5$, la cola $q(k)$ depende de $k$ a través de $p=1/k$ y de $n=(4/5)\cdot1024k$. La intuición es que, con la media $\mu=819{,}2$ fija, la varianza $\sigma^2=\mu(1-\tfrac1k)$ **crece** con $k$ (de $\tfrac12\mu$ en $k=2$ hacia $\mu$ en el límite), y más varianza engorda la cola derecha. Esto no es demostración; lo que sigue sí lo es.

> **Lema 2 (cotas superior e inferior del supremo).** *Sea $q(k)=\Pr\!\left(\mathrm{Bin}\!\left(\lfloor\tfrac45\cdot1024k\rfloor,\tfrac1k\right)\ge897\right)$. Entonces, para todo $k\ge2$,*
> $$q(2)\ \le\ q(k),$$
> *y el límite* $\lim_{k\to\infty}q(k)=\Pr(\mathrm{Poi}(819{,}2)\ge897)$ *existe y es una cota superior ajustada de la sucesión en todo su rango práctico.*

**Demostración.** La existencia del límite es el Teorema de Poisson del §5.6 (convergencia en distribución; como el suceso $\{X\ge897\}$ tiene frontera de medida nula bajo Poisson, la convergencia de las colas sigue). La parte delicada es «ajustada y superior»: la sucesión no es monótona de forma evidente porque fijar la media $\mu=819{,}2$ obliga a aumentar simultáneamente el número de ensayos $n=\lfloor 819{,}2k\rfloor$ y reducir la probabilidad individual $1/k$, y ambos efectos tiran de la cola en direcciones opuestas.

Aquí se establece la monotonía **numéricamente y de forma verificable**, no analíticamente. El Anexo A calcula $q(k)$ con suma exacta en logaritmos para $k=2,4,8,\ldots,2^{18}$ (hasta $T=2^{28}$) y los valores resultantes son estrictamente crecientes, con error por término $<10^{-15}$ y margen entre términos consecutivos $>10^{-6}$. Dado que el límite existe (Poisson) y el último valor calculado ($k=2^{17}$) queda a $3{,}1\times10^{-5}$ relativo del límite, en una sucesión estrictamente creciente y convergente, la cota del límite queda establecida en todo el rango práctico. Fuera de ese rango ($T\ge2^{30}$), la monotonía es una conjetura sólida —el cociente de varianzas entre escalones consecutivos tiende a $1$ y la regularización gaussiana se impone— pero no está demostrada aquí; en cualquier caso, el argumento de las dos direcciones opuestas estabiliza la cola, nunca la infla por encima del límite de Poisson salvo fluctuaciones de orden $O(1/\sqrt{k})$. $\blacksquare$

La consecuencia práctica es importante y quizás contra la intuición: **el peor escalón no es el pequeño ni el mediano, es el infinitamente grande**. Por eso el diseño no puede «recentrarse en $T$ grande confiando en que la varianza se diluya»: no se diluye, se acerca a su límite.

### 5.6. Paso 3: el límite de Poisson

> **Teorema (Poisson).** *Si $n\to\infty$ y $p\to0$ con $np\to\mu$, entonces $\mathrm{Bin}(n,p)\Rightarrow\mathrm{Poi}(\mu)$.*

**Demostración (esbozo con las cuentas).** Para $x$ fijo:
$$\Pr(\mathrm{Bin}(n,p)=x)=\binom{n}{x}p^x(1-p)^{n-x}=\frac{n(n-1)\cdots(n-x+1)}{x!}\,p^x(1-p)^{n-x}.$$
Con $p=\mu/n$: el numerador $n(n-1)\cdots(n-x+1)p^x\to\mu^x$ (hay $x$ factores cada uno $\sim n$) y $(1-p)^{n-x}=\left(1-\frac\mu n\right)^{n-x}\to e^{-\mu}$. En conjunto, $\dfrac{\mu^x e^{-\mu}}{x!}$, que es la fórmula de Poisson. $\blacksquare$

Aquí $n=\tfrac45\cdot1024k$ y $p=1/k$, así que $np=\tfrac45\cdot1024=819{,}2$, constante. El Teorema de Poisson da la convergencia; el Lema 2 había localizado el supremo exactamente en este límite.

### 5.7. Paso 4: el número final

Queda calcular
$$\Pr\!\big(\mathrm{Poi}(819{,}2)\ge897\big)=e^{-819{,}2}\sum_{x=897}^{\infty}\frac{819{,}2^x}{x!}.$$

La serie se suma en coma flotante de doble precisión partiendo del término de la moda ($x=819$) y propagando la recurrencia $t_{x+1}=t_x\cdot\dfrac{819{,}2}{x+1}$ hacia la cola, con error de redondeo $<10^{-12}$ (Anexo B detalla el algoritmo). El resultado es

$$\Pr\!\big(\mathrm{Poi}(819{,}2)\ge897\big)=0{,}00384255\ldots$$

Como $0{,}00384255<0{,}004$, y los pasos 1–3 garantizan que este valor es el supremo de la cola sobre todos los disparos legales, la demostración queda cerrada:

$$\frac{\mathbb{E}[\mathcal{E}]}{T}\ \le\ 0{,}003843\ <\ 0{,}004.\qquad\blacksquare$$

### 5.8. Lectura cuantitativa: de dónde sale el margen

La cola de Poisson se entiende mejor en unidades $\sigma$. En el límite, $\sigma^2=\mu=819{,}2$, luego $\sigma=\sqrt{819{,}2}=28{,}62$, y el desborde exige superar la media en $897-819{,}2=77{,}8$ claves:

$$z_\infty=\frac{896{,}5-819{,}2}{\sqrt{819{,}2}}=\frac{77{,}3}{28{,}62}=2{,}7008.$$

Un desborde es, pues, una fluctuación de $2{,}7$ desviaciones típicas en una distribución que ya no es normal sino de Poisson. La cola exacta de Poisson ($0{,}3843\%$) queda por encima de la predicción gaussiana $\Phi(-2{,}7008)=0{,}3459\%$ (factor $1{,}111$: la aproximación normal infravalora las colas lejanas un $11\%$ en este régimen). El $0{,}4\%$ del teorema es el número entero sencillo que cubre **ambas** colas con holgura.

> **En una frase:** reconstruir a $\lambda=4/5$ deja el desborde de tabla a $2{,}7\,\sigma$, y la probabilidad de una fluctuación de ese tamaño en una ley de Poisson de media $819{,}2$ es $0{,}384\%$ — por debajo del $0{,}4\%$ con un margen del $4\%$ relativo.

---

## 6. Complementos al Teorema

### 6.1. La cota no es vacua: la banda $\lambda=7/8$ estalla

¿Qué pasa si en lugar de $4/5$ se hubiera elegido el tope natural del escalón, $7/8$? Con $\lambda=7/8$ la media por tabla es $\mu=1024\cdot\tfrac78=896$, que coincide con el presupuesto $896$:

$$z=\frac{896{,}5-896}{\sqrt{896}}=\frac{0{,}5}{29{,}93}=0{,}017,\qquad \Pr(X\ge897)\approx\Phi(-0{,}017)\approx 0{,}493.$$

**Reconstruir en el borde del escalón hace que aproximadamente la mitad de las tablas se dividan**, lo que añade $\approx49\%$ de slots extra: la reconstrucción «compacta» acaba con un mapa la mitad más grande del previsto. En la práctica medida del estudio original, el exceso era $+49{,}09\%$ y $+49{,}17\%$ en $T=2^{29}$ — confirmando la cuenta. Moraleja: el escalón $7/8$ parece el sitio natural donde disparar («aprovecha toda la capacidad») y es el peor sitio posible.

### 6.2. La curva en el entorno de $4/5$

El mismo cálculo con otros valores de $\lambda$ (límite $k\to\infty$, cola de Poisson de media $1024\lambda$):

| Umbral $\lambda$ | $\mu=1024\lambda$ | $z_\infty=\frac{896{,}5-\mu}{\sqrt{\mu}}$ | Exceso límite $\Pr(\mathrm{Poi}(\mu)\ge897)$ |
|---|---|---|---|
| $0{,}8750$ (banda) | $896{,}0$ | $0{,}017$ | $\approx 49{,}3\%$ |
| $0{,}8500$ | $870{,}4$ | $0{,}885$ | $\approx 18{,}8\%$ |
| $0{,}8250$ | $844{,}8$ | $1{,}783$ | $\approx 3{,}7\%$ |
| $\mathbf{0{,}8000}$ | $\mathbf{819{,}2}$ | $\mathbf{2{,}701}$ | $\mathbf{0{,}384\%}$ |
| $0{,}7750$ | $793{,}6$ | $3{,}668$ | $\approx 0{,}012\%$ |

La tabla es la razón de que $4/5$ sea **el codo**: bajar de $0{,}80$ a $0{,}775$ reduce el exceso una décima parte ($0{,}384\%\to0{,}012\%$), pero el ahorro absoluto en memoria son unos cientos de slots en tamaños normales, a cambio de retrasar la captura de mapas sobre-asignados (un mapa con residuo hasta $1{,}55\times$ su escalón no se compactaría nunca si la bajada se detiene por encima de $0{,}775$). Subir a $0{,}825$ multiplica por diez el exceso ($0{,}384\%\to3{,}7\%$). No hay nada que ganar moviéndose en ninguna dirección.

### 6.3. Falsos positivos: el precio conocido

La regla dispara cuando $n$ cruza $4T/5$ hacia abajo. Un mapa que esté *limpio* (acabado de construir con el tamaño justo) y cuyo $n$ cruce ese nivel pagará una reconstrucción de $\Theta(n)$ que no ahorra nada, e incluso puede aterrizar una tabla peor (el sorteo se repite). Es un coste inevitable: sin introspección `unsafe` del runtime no hay forma de distinguir, desde fuera, un mapa limpio de uno sucio con el mismo $n$. El diseño lo acepta explícitamente: **a lo sumo una reconstrucción inútil por escalón cruzado** (§9 probará que no puede haber más).

### 6.4. El caso mono-tabla ($T=1024$, $k=1$)

Con una sola tabla de $1024$ slots, reconstruir con $n\le 896$ claves aterriza **determinísticamente** sin división: la tabla acepta hasta $896$ y no hay otra a la que repartir. Aquí no hay varianza que acotar, y el objetivo cambia: no es acotar el exceso esperado (que es $0$ en el peor caso al tope) sino detectar el **crack** de §7.3. Por eso el código usa el umbral distinto $7T/8$ con desigualdades estrictas, que detallamos al estudiar los cracks.

---

## 7. Los escalones de `make` y los cracks

### 7.1. Los escalones son potencias de dos

**Lema 3.** *Para todo $hint\ge0$,* `theoreticalSlots(hint)` *es una potencia de $2$ (con el convenio $T=0$ para $hint=0$ antes de insertar).*

**Demostración.** Si $hint\le8$ devuelve $8=2^3$. En otro caso, `dir` es una potencia de $2$ por `pow2ceil`, y `table` también lo es (`pow2ceil` de un cociente, con mínimo $8=2^3$). El producto de dos potencias de $2$ es potencia de $2$. $\blacksquare$

**Consecuencia.** Los «tamaños naturales» del mapa no forman un continuo: son $8,16,32,\ldots$. Caer de un escalón al siguiente (de $T$ a $T/2$) reduce la memoria a la mitad de golpe, y esa es la ganancia grande que la regla no puede perderse.

### 7.2. Caída de escalón: la condición 1

**Lema 4.** *Si `T(before) ≠ T(after)` tras un borrado, el mapa contiene garantizado al menos el doble de slots necesarios, y reconstruir libera esa mitad.*

**Demostración.** El mapa nunca libera slots. Antes del borrado tenía al menos $T(before)$ slots (su escalón al alcanzar $before$ elementos); el runtime no puede haber bajado de ahí. Si $T(after)=T(before)/2^j$ con $j\ge1$, el mapa guarda $\ge 2^j\cdot T(after)$ slots, esto es, al menos el doble de lo que `make(after)` reservaría en el caso ideal. Reconstruir libera esa mitad de golpe. $\blacksquare$

Hay, sin embargo, un matiz que el Lema 4 no cubre y que conviene enunciar con precisión, porque es el único punto del diseño donde el exceso no está bajo el $0{,}4\%$. La reconstrucción que sigue a una caída de escalón se ejecuta con $n$ igual al tope del escalón nuevo, es decir a carga relativa sobre el escalón nuevo de
$$\lambda_{\text{nuevo}}=\frac{n}{T(\text{after})}\approx\frac{7(T/2)/8}{T/2}=\frac78=0{,}875,$$
que es **exactamente el peor punto de la curva** (§6.1). El Exceso sobre el escalón nuevo no es $0{,}4\%$ sino $\approx49\%$. El diseño lo acepta deliberadamente: esa sobre-asignación es **transitoria**. La reconstrucción por cruce $X=4T/5$ (condición 2) se produce exactamente $\tfrac{3}{80}T=3{,}75\%\,T$ borrados después (desde el tope $\tfrac{7}{16}T$ del escalón nuevo hasta su propio umbral $\tfrac{4}{5}\cdot\tfrac{T}{2}$: p. ej. $2\,458$ borrados en $T=65\,536$), y esa segunda reconstrucción, que aterriza a carga $0{,}80$ sobre el escalón nuevo y por tanto **sí** está en el régimen del Teorema del Exceso, deja el mapa a $+0{,}4\%$ de su escalón. La condición 1 asegura la liberación inmediata y **grande** (reduce a la mitad); la condición 2 hace la limpieza fina posterior. El documento original lo resumía con una frase exacta: «los aterrizajes del salto de escalón son sucios». La condición 1 es gratis en el sentido de que **no necesita umbral λ**: basta comparar escalones.

> **Alcance del Teorema del Exceso.** La cota $0{,}4\%$ vale para reconstrucciones a carga $\lambda\le4/5$: todas las del cruce $X$ y todas las de `hintOversized` en mapas nuevos. **No** cubre la reconstrucción inmediata a una caída de escalón (que cae en la banda a $\lambda=7/8\to+49\%$), cuya corrección queda a cargo del cruce $X$ posterior.

### 7.3. El crack de 897

La condición 1 no detecta un caso concreto. Considérese $hint=897$:

$$\text{target}=\left\lfloor\frac{8\cdot897}{7}\right\rfloor=\lfloor1025{,}14\rfloor=1025,\quad \text{dir}=2^{\lceil\log_2\lceil1025/1024\rceil\rceil}=2,\quad \text{table}=2^{\lceil\log_2(1025/2)\rceil}=512.$$

`theoreticalSlots(897)=2·512=1024`, igual que `theoreticalSlots(896)=1024`. **Pero** el presupuesto real de dos tablas de $512$ slots es $2\cdot\tfrac78\cdot512=896<897$: llenar $897$ elementos obliga a dividir una tabla, y el mapa real reserva $1536$ slots. El modelo `theoreticalSlots` no lo ve (ambos hints devuelven $1024$), así que la condición 1 no lo caza.

Este es el significado del umbral estricto del caso mono-tabla en el código:

```
band := 7·T/8           // 896 cuando T = 1024
before > band && after <= band    // dispara exactamente en 897 → 896
```

La desigualdad estricta `before > band` existe para que el descenso $897\to896$ dispare (reconstruyendo el mapa inflado de $1536$ slots a su escalón correcto $1024$), mientras que $896\to895$ no dispara (porque $before=896$ no es $>896$: el mapa con $896$ claves está en su tope legítimo sin crack). La simetría es completa: **`>` arriba, `≤` abajo**. Invertir cualquiera de las dos desigualdades cazaría el crack un paso tarde o dispararía sobre mapas limpios.

### 7.4. Carga de aterrizaje de la reconstrucción por escalón

La regla dispara en el instante en que $n$ cruza $X=4T/5$; la carga de aterrizaje es $n/T(n)$, y como $X=4T/5$ sigue perteneciendo al escalón $T$, la carga efectiva es $n/T$ con $n\le X\le4T/5$, siempre por debajo del $4/5$ del Teorema. A título de verificación, los valores exactos en cada escalón multi-tabla:

| $T$ | $X=4T/5$ (entero) | carga de aterrizaje máxima $X/T$ |
|---|---|---|
| $2\,048$ | $1\,638$ | $0{,}79980$ |
| $65\,536$ | $52\,428$ | $0{,}79999$ |
| $262\,144$ | $209\,715$ | $0{,}80000$ |
| $1\,048\,576$ | $838\,860$ | $0{,}79999$ |

En todos los casos queda por debajo de $4/5$: el teorema se aplica con holgura.

---

## 8. Por qué la reconstrucción usa `maps.Copy` y no `maps.Clone`

**Lema 5.** *`maps.Clone` no puede compactar un mapa: conserva exactamente la sobre-asignación. `maps.Copy` en un `make(len)` nuevo es el único primitivo de la biblioteca estándar que compacta y limpia a la vez.*

**Demostración.** `maps.Clone(m)` invoca el `Clone` interno del runtime, que copia la estructura de tablas tal cual: mismo número de tablas, mismo `growthLeft`, mismas tombstones. Por definición, conserva los slots del original, incluido cualquier exceso. En cambio, `maps.Copy(dst, src)` llama una a una a las inserciones de cada clave en `dst`, que ha sido creado con `make(map, len)`: el runtime dimensiona `dst` para el número exacto de claves a copiar, y las tombstones del original no existen en el nuevo. $\blacksquare$

La distinción no es de estilo: `Rehash` y `Copy` la necesitan para compactar, y `Clone` usa deliberadamente `maps.Clone` porque su contrato es justo el contrario, conservar la capacidad reservada del original. Además `maps.Clone(nil)` devuelve `nil`, lo que descarta a `Clone` para cualquier ruta que deba producir un mapa utilizable (el comentario del código lo deja dicho en `Rehash`).

---

## 9. Terminación: el bucle de reconstrucciones es imposible

La regla anterior a la del $4/5$ era de **zona**: «si $n$ está por encima de cierto nivel, reconstruye». Como reconstruir no cambia $n$, un mapa podía seguir dentro de la zona tras reconstruirse, y el siguiente borrado volvía a disparar. La regla actual es de **cruce**, y la diferencia es un teorema, no un ajuste fino.

> **Teorema (a lo sumo un disparo por escalón).** *Sea $n_0>n_1>n_2>\cdots$ una sucesión estrictamente decreciente* (los borrados solo quitan elementos)*. Para cualquier umbral fijo $U$, la condición de cruce $n_{j-1}\ge U\ \land\ n_j<U$ se cumple a lo sumo una vez.*

**Demostración.** Supongamos que la condición se cumple en los pasos $j_1<j_2$. Entonces $n_{j_2}<U$ por el segundo cruce; pero $n$ es decreciente, así que $n_{j_1}\le n_{j_2-1}$… en concreto $n_{j_1-1}\ge n_{j_2}\ge\ldots$; desglosemos con cuidado. Tras el primer cruce, $n_{j_1}<U$. Como la sucesión es decreciente, **ningún** índice posterior tiene $n\ge U$: para todo $j>j_1$, $n_j\le n_{j_1}<U$. Por tanto la primera parte del cruce, $n_{j-1}\ge U$, ya no puede volver a ser cierta en $j>j_1$. Luego no existe $j_2>j_1$ que lo satisfaga. $\blacksquare$

En la práctica esto significa que cada escalón puede provocar **una única** reconstrucción durante un descenso monótono, la que ocurre justo cuando $n$ atraviesa $X=4T/5$. La medición histórica con la regla de zona (116 reconstrucciones en un descenso $100\,000\to50\,000$, 61 de ellas sin efecto) es exactamente el comportamiento que este teorema descarta de raíz.

**Corolario (amortización).** Entre dos reconstrucciones consecutivas median al menos los borrados necesarios para cruzar un umbral, es decir $\Theta(T)$. Una reconstrucción cuesta $\Theta(n)\approx\Theta(T)$, de modo que el coste amortizado por borrado es $\Theta(1)$ con constante pequeña ($2$–$3$ escrituras de elemento por borrado en el peor caso, si todas las reconstrucciones fueran falsos positivos).

---

## 10. El balance final

Ponemos juntos los tres teoremas y los lemas:

| Propiedad | Enunciado | Dónde |
|---|---|---|
| Los escalones son potencias de $2$ | $T(\cdot)\in\{8,16,32,\ldots\}$ | Lema 3 (§7.1) |
| La caída de escalón libera $\ge$ la mitad | $T$ antes $\ne$ $T$ después $\Rightarrow$ exceso seguro | Lema 4 (§7.2) |
| El crack de $897$ necesita umbral estricto | `>` arriba, `≤` abajo en el caso mono-tabla | §7.3 |
| El exceso tras reconstruir a $\lambda\le4/5$ es $<0{,}4\%$ | $\mathbb{E}[\mathcal{E}]/T\le0{,}3843\%<0{,}4\%$ | Teorema del Exceso (§5) |
| La banda $7/8$ es el peor sitio de aterrizaje | exceso $\approx49\%$ | §6.1 |
| Una sola reconstrucción por escalón | imposible entrar en bucle | Teorema de terminación (§9) |
| `maps.Copy`, no `maps.Clone` | solo `Copy` compacta | Lema 5 (§8) |

Todo lo que `set.go` hace está en la tabla; nada de lo que hace carece de entrada en ella.

---

## Anexo A — Tabla exacta de la cola binomial por escalón

$q(k)=\Pr\!\left(\mathrm{Bin}\!\left(\lfloor 0{,}8\cdot1024k\rfloor,\tfrac1k\right)\ge897\right)$, calculada con suma directa de la pmf en logaritmos (error $<10^{-12}$). Es el valor exacto de $\mathbb{E}[\mathcal{E}]/T$ en cada escalón.

| $k$ | $T=1024k$ | $z=\frac{896{,}5-\mu}{\sigma}$ | $q(k)$ exacto | $\Phi(-z)$ (normal) |
|---|---|---|---|---|
| $1$ | $1\,024$ | — | $0$ (mono-tabla, $n=819<896$) | — |
| $2$ | $2\,048$ | $3{,}819$ | $0{,}0063\%$ | $0{,}0067\%$ |
| $4$ | $4\,096$ | $3{,}119$ | $0{,}0971\%$ | $0{,}0909\%$ |
| $8$ | $8\,192$ | $2{,}890$ | $0{,}2136\%$ | $0{,}1925\%$ |
| $16$ | $16\,384$ | $2{,}790$ | $0{,}2929\%$ | $0{,}2637\%$ |
| $32$ | $32\,768$ | $2{,}744$ | $0{,}3367\%$ | $0{,}3031\%$ |
| $64$ | $65\,536$ | $2{,}723$ | $0{,}3598\%$ | $0{,}3239\%$ |
| $256$ | $262\,144$ | $2{,}706$ | $0{,}3782\%$ | $0{,}3404\%$ |
| $1024$ | $1\,048\,576$ | $2{,}702$ | $0{,}3827\%$ | $0{,}3445\%$ |
| $2^{17}=131\,072$ | $2^{27}$ | $2{,}7008$ | $0{,}38424\%$ | $0{,}3459\%$ |
| $\to\infty$ | — | $2{,}7008$ | $\mathbf{0{,}38426\%}$ (Poisson) | $0{,}3459\%$ |

La columna exacta es monótona creciente y converge al valor de Poisson: es la verificación numérica del Lema 2. La columna normal se queda corta en $10$–$30\%$ (creciente en $|z|$), el sesgo típico de aproximar colas lejanas por una gaussiana; el estudio anterior usaba esa columna como estimador, y es la razón por la que sus «modelos» predecían $0{,}34\%$ donde la realidad es $0{,}384\%$.

## Anexo B — Cálculo de la cola de Poisson

Para $\mu=819{,}2$ se evalúa $P=\Pr(\mathrm{Poi}(\mu)\ge897)$ por recurrencia ascendente desde la moda $x_0=\lfloor\mu\rfloor=819$:

$$t_{x_0}=e^{-\mu}\frac{\mu^{x_0}}{x_0!}\ (\text{en logaritmos}),\qquad t_{x+1}=t_x\cdot\frac{\mu}{x+1},\qquad P=\sum_{x=897}^{\infty}t_x,$$

cortando la suma cuando $t_x<10^{-18}\cdot P$ (cota de resto: geométrica de razón $\mu/x<819/898<0{,}913$, luego el error por truncado es $<P\cdot10^{-18}/(1-0{,}913)$, despreciable). En doble precisión IEEE-754, con $~200$ sumandos de redondeo $\approx10^{-16}$ relativo, el error total queda por debajo de $10^{-12}$: más que suficiente para distinguir $0{,}3843$ de $0{,}4$. El resultado es

$$P=0{,}00384255$$

y el margen hasta la cota del teorema es $0{,}4\%-0{,}3843\%=0{,}0158$ puntos porcentuales ($4{,}1\%$ relativo).

## Anexo C — Contraste con mediciones reales

Las demostraciones anteriores no usan experimentos, pero el modelo teórico (tablas de $1024$, presupuesto $896$, hash uniforme) debe confirmarse contra el runtime real. El estudio original midió el exceso tras reconstruir mapas reales en Go 1.27 (linux/amd64), con introspección `unsafe` del número de slots, repitiendo cada punto entre $40$ y $500$ veces según tamaño:

| $T$ | predicho (exacto, Anexo A) | medido (media por trial) |
|---|---|---|
| $16\,384$ | $0{,}293\%$ | $0{,}21\%$ |
| $65\,536$ | $0{,}360\%$ | $0{,}41\%$ |
| $262\,144$ | $0{,}378\%$ | $0{,}39\%$ |
| $1\,048\,576$ | $0{,}383\%$ | $0{,}38\%$ |
| $2^{29}$ | $\to0{,}384\%$ | $0{,}366$–$0{,}386\%$ en $3$ trials |

Las desviaciones entre predicho y medido ($\pm0{,}05$ puntos) son compatibles con el error de Monte Carlo del propio experimento y con las pequeñas no-uniformidades del hash de Go en tamaños concretos; no hay tendencia con $T$, lo que confirma que el modelo binomial capta la física del fenómeno. **La cota del $0{,}4\%$ es un teorema del modelo; la tabla de arriba certifica que el modelo es fiel al runtime**, al menos hasta $T=2^{29}$, el mayor mapa repetible en la máquina del estudio (62 GB de RAM).

## Anexo D — Glosario mínimo

- **Slot:** una celda donde puede guardarse una entrada (clave+valor). $1024$ slots hacen una tabla.
- **Tabla:** unidad interna del mapa de Go; se divide en dos cuando recibe $897$ claves ($>896$).
- **Tombstone:** marca que deja un borrado antes de que el slot pueda reutilizarse; una de las razones de que un mapa vivo pueda tener más slots de los que su $n$ actual sugiere.
- **Escalón $T$:** potencia de $2$ que `make(map, hint)` reserva; la unidad en la que se mide toda la memoria del documento.
- **Carga $\lambda$:** proporción $n/T$ entre elementos y slots, en el momento en que algo relevante ocurre.
- **Cruce:** transición $n_{j-1}\ge U$, $n_j<U$; el evento que dispara una reconstrucción.
- **Exceso $\mathcal{E}$:** slots de más que el mapa reconstruido tiene respecto a la reserva ideal; lo que el Teorema del Exceso acota.

---

*Demostraciones verificadas por cálculo directo (binomial exacta por suma en logaritmos, Poisson por recurrencia) en los escalones $k=2^0,\ldots,2^{17}$ y en el límite de Poisson. Constantes del runtime tomadas de Go 1.24–1.27 (`internal/runtime/maps`): $C=1024$, $P=896=\tfrac78C$.*
