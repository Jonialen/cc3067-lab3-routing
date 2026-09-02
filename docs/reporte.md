# Laboratorio 3 — Algoritmos de Enrutamiento

**Universidad del Valle de Guatemala**
**CC3067 — Redes**
**Fecha de entrega:** 3 de septiembre de 2026

**Integrantes del grupo:**

| Nombre | Carné |
| --- | --- |
| _(completar)_ | _(completar)_ |
| _(completar)_ | _(completar)_ |
| _(completar)_ | _(completar)_ |
| _(completar)_ | _(completar)_ |

**Repositorio:** _(completar con el enlace)_

---

## 1. Descripción de la práctica

El laboratorio consiste en construir una red simulada en la que cada nodo es un
proceso independiente que se comunica con sus vecinos mediante sockets TCP.
Cada nodo conoce inicialmente solo a sus vecinos directos y debe construir su
tabla de enrutamiento para poder entregar mensajes a destinos con los que no
tiene un enlace directo.

Se implementaron tres algoritmos intercambiables sobre la misma infraestructura
de red:

1. **Dijkstra**, que calcula caminos mínimos a partir de una topología conocida.
2. **Flooding**, que reenvía cada paquete a todos sus vecinos.
3. **Link State Routing (LSR)**, que descubre la topología intercambiando
   anuncios de estado de enlace y luego aplica Dijkstra sobre ella.

La implementación se realizó en Go. La elección respondió a tres criterios: el
modelo de concurrencia mediante goroutines corresponde directamente a la
separación entre plano de forwarding y plano de ruteo que exige el enunciado; el
compilador produce un binario estático único, lo que resuelve la portabilidad
entre plataformas sin gestores de dependencias; y la biblioteca estándar cubre
sockets TCP y serialización JSON, por lo que el proyecto no tiene dependencias
externas.

---

## 2. Descripción de los algoritmos utilizados y su implementación

### 2.1 Arquitectura general

El enunciado señala que Dijkstra y Flooding se utilizan dentro de LSR y que por
ello se requiere alta modularidad. Este requisito determinó la arquitectura del
proyecto: ambos algoritmos se implementaron como **funciones puras**, sin
conocimiento alguno de sockets ni de paquetes en tránsito.

```go
func Dijkstra(g Graph, source string) Table
func Flood(neighbors []string, pkt *protocol.Packet) []string
```

De esta forma existe una sola implementación de cada algoritmo en todo el
proyecto. El modo `dijkstra` y el modo `flooding` invocan esas funciones
directamente, y LSR invoca las mismas: `Flood` para diseminar sus anuncios y
`Dijkstra` para derivar la tabla de forwarding a partir de la base de datos de
enlaces.

La organización del código es la siguiente:

```
cmd/node/            punto de entrada del proceso
internal/protocol/   envelope JSON y payload de estado de enlace
internal/transport/  servidor y cliente TCP
internal/config/     carga de topología y tabla de nombres
internal/routing/    plano de ruteo (grafo, flooding, algoritmos)
internal/node/       plano de forwarding y descubrimiento de vecinos
internal/cli/        consola interactiva
```

Cada algoritmo implementa la interfaz `Algorithm`, que aísla las decisiones de
ruteo del transporte:

```go
type Algorithm interface {
    Proto() protocol.Proto
    Start(ctx context.Context)
    Forward(pkt *protocol.Packet) []string
    HandleInfo(pkt *protocol.Packet)
    OnLinkUp(neighbor string, cost float64)
    OnLinkDown(neighbor string)
    Table() Table
}
```

El detalle relevante es que `Forward` devuelve una lista de vecinos. Esa firma
unifica flooding, que reenvía a varios destinos, con Dijkstra y LSR, que
reenvían a uno solo, sin que el plano de forwarding necesite distinguir entre
algoritmos.

Recíprocamente, el nodo expone a los algoritmos únicamente la interfaz
`Fabric` (`ID`, `Neighbors`, `SendTo`, `Logf`). Un algoritmo no puede abrir
conexiones ni conocer direcciones: solo decide hacia qué vecino lógico va un
paquete.

### 2.2 Concurrencia: los dos planos

El enunciado exige que forwarding y routing corran en paralelo. Ambos se
implementaron como goroutines que se comunican por canales, no por memoria
compartida:

- **Plano de forwarding.** Las goroutines de conexión decodifican cada línea
  JSON y depositan el paquete en un canal (`inbox`). Una única goroutine drena
  ese canal y procesa los paquetes. Con esto, un recálculo de rutas nunca
  bloquea un socket, y el manejo de paquetes no requiere candados propios.
- **Plano de ruteo.** Una goroutine sondea periódicamente a los vecinos
  mediante paquetes `hello` y declara caído al que deja de responder. El
  algoritmo activo recibe eventos `OnLinkUp` / `OnLinkDown` en lugar de
  consultar el estado.

La tabla de enrutamiento se protege con `SafeTable`, que utiliza un
`sync.RWMutex`: el plano de ruteo la reemplaza completa, el de forwarding la
consulta por paquete.

### 2.3 Protocolo

Cada paquete es un objeto JSON terminado en salto de línea sobre TCP. El
delimitado por línea mantiene el framing trivial y permite inspeccionar un nodo
con `nc`.

```json
{
  "proto": "lsr",
  "type": "message",
  "from": "A",
  "to": "I",
  "ttl": 8,
  "headers": [{"msg_id": "3f2a..."}, {"hop": "C"}, {"path": "A>C>F"}],
  "payload": "hola"
}
```

Tipos de paquete implementados:

| Tipo | Función |
| --- | --- |
| `hello` | Sondea a un vecino; el receptor responde con `echo`. |
| `echo` | Devuelve el timestamp original para medir el viaje de ida y vuelta. |
| `info` | Transporta un paquete de estado de enlace (LSP). |
| `message` | Datos de usuario; se reenvía o se imprime al llegar a destino. |

Encabezados propios. Los encabezados desconocidos provenientes de otras
implementaciones se preservan intactos al reenviar un paquete, lo cual es
necesario para la interoperabilidad entre grupos.

| Encabezado | Propósito |
| --- | --- |
| `msg_id` | Identificador único, para descartar duplicados. |
| `hop` | Nodo anterior, para no devolver el paquete por donde llegó. |
| `sent_at` | Timestamp en nanosegundos, para medir el costo del enlace. |
| `path` | Traza de los nodos recorridos. |

El decodificador tolera paquetes de otros grupos que omitan campos: un paquete
sin `ttl` recibe el valor por defecto y uno sin `msg_id` recibe uno generado, ya
que sin identificador no es posible detectar duplicados.

### 2.4 Dijkstra

**Entrada:** la topología completa (nodos y aristas), leída de un archivo de
configuración.

Se implementó con una cola de prioridad (`container/heap`), lo que da una
complejidad de O((V + E) log V) frente a O(V²) de la versión con búsqueda
lineal del mínimo.

La particularidad respecto a la formulación de libro es que una tabla de
enrutamiento no necesita el camino completo, sino únicamente el **primer
salto**. Por eso, además de las distancias, el algoritmo propaga `firstHop`:
cuando se relaja una arista desde el nodo origen, el primer salto es el vecino
mismo; en cualquier otro caso se hereda el primer salto del nodo desde el que se
llegó.

Los vecinos se recorren en orden alfabético al relajar aristas. Esto no afecta
la corrección, pero hace que la elección entre caminos de igual costo sea
reproducible entre ejecuciones y entre nodos, lo que facilita interpretar las
trazas.

En modo `dijkstra` la tabla se calcula una sola vez al arrancar. Aun así, el
nodo reacciona a fallas: al detectar que un vecino dejó de responder, poda sus
enlaces de la topología y recalcula, de modo que no se convierte en un agujero
negro para el tráfico.

### 2.5 Flooding

**Entrada:** únicamente el conocimiento de sus vecinos.

Cada paquete de datos se reenvía a todos los vecinos excepto a aquel del que se
recibió. No se mantiene tabla de enrutamiento alguna.

El problema central del flooding es la terminación: en una topología con ciclos
—como la utilizada en el laboratorio— el número de copias crece
exponencialmente. Se implementaron tres mecanismos independientes, porque en la
prueba en clase la red incluye implementaciones de otros grupos y no se puede
depender de un solo mecanismo:

1. **Caché de `msg_id`** (`SeenCache`). Un paquete ya procesado se descarta. Las
   entradas expiran tras un tiempo, de modo que una ejecución larga no consume
   memoria sin límite. Este es el mecanismo que efectivamente corta los ciclos.
2. **Números de secuencia**, aplicables a los anuncios de estado de enlace: un
   LSP cuya secuencia no supera a la almacenada no se vuelve a difundir.
3. **TTL**, un presupuesto fijo de saltos, como último recurso frente a un nodo
   externo con comportamiento incorrecto.

Adicionalmente se aplica *split horizon*: nunca se reenvía por el enlace del que
se recibió el paquete. No es necesario para la corrección, ya que la caché de
duplicados basta, pero reduce a la mitad el tráfico por enlace.

### 2.6 Link State Routing

**Entrada:** sus vecinos directos, y los anuncios de los demás nodos, de los
cuales se deriva la topología.

El ciclo de operación es el siguiente:

1. **Descubrimiento.** El nodo envía `hello` a sus vecinos configurados. El
   receptor responde con `echo` devolviendo el `sent_at` original, lo que
   permite al emisor medir el viaje de ida y vuelta sin mantener estado por
   sonda. El costo del enlace es la mitad de ese tiempo, es decir el retardo en
   un sentido.
2. **Anuncio.** El nodo construye un *link state packet* con su identificador,
   un número de secuencia y el conjunto de sus vecinos con sus costos. Solo
   anuncia lo que puede medir directamente, que es la propiedad que define a un
   protocolo de estado de enlace.
3. **Difusión.** El LSP se disemina con la función `Flood`, la misma que utiliza
   el modo flooding.
4. **Base de datos.** Cada nodo almacena el anuncio más reciente de cada origen.
   Un anuncio con secuencia menor o igual a la almacenada se descarta y no se
   redifunde; este es el mecanismo que hace converger la difusión.
5. **Cálculo.** Con la topología reconstruida a partir de la base de datos se
   invoca `Dijkstra`, obteniendo la tabla de forwarding.
6. **Envejecimiento.** Los anuncios se refrescan periódicamente. Un anuncio que
   deja de refrescarse expira y su origen se elimina de la topología, que es
   como se detecta la caída de un nodo no adyacente.

#### Control del tráfico de señalización

Durante las pruebas se detectó un defecto de diseño que solo se manifiesta con
la red completa en ejecución. Los costos de enlace provienen de mediciones
reales, y en un enlace rápido el tiempo de ida y vuelta varía más del 50 % entre
una sonda y la siguiente. Como la condición inicial para reanunciar era un
cambio relativo del 25 %, cada nodo reanunciaba su estado en cada `hello`,
generando una tormenta de paquetes de control que crece con el tamaño de la red.

Se corrigió con dos mecanismos:

1. **Suavizado.** El plano de descubrimiento mantiene un promedio móvil
   exponencial del tiempo de ida y vuelta de cada enlace, de modo que una
   muestra ruidosa no altera significativamente el costo.
2. **Política de anuncio.** La aparición o desaparición de un vecino es un
   cambio de topología y se difunde de inmediato. Un costo que solo fluctuó se
   anuncia únicamente si superó un umbral y nunca con más frecuencia que el
   límite de tasa establecido; el refresco periódico lo transporta de todos
   modos.

El efecto se midió sobre la red de nueve nodos en contenedores y se documenta en
la sección de resultados.

---

## 3. Resultados

### 3.1 Entorno de pruebas

Las pruebas se ejecutaron en tres entornos:

1. **Suite automatizada.** Pruebas unitarias de los algoritmos y pruebas de
   integración que levantan nodos reales sobre sockets de loopback. Se ejecutan
   con `make test` y bajo el detector de condiciones de carrera con `make race`.
2. **Nueve procesos locales**, uno por nodo, sobre la topología de referencia.
3. **Nueve contenedores Docker**, uno por nodo, sobre una red bridge privada.
   Cada nodo tiene su propia pila de red y su propia dirección, lo que se
   aproxima a la prueba en clase.

Topología de referencia utilizada:

```
A — B — D — E
|  /|   |   |
| / |   |   |
C   |   F — G — I
 \  |  /|      /
  \ | / |     /
    ·   H ———
```

Adyacencias: A(B,C) B(A,C,D) C(A,B,F) D(B,E,F) E(D,G) F(C,D,G,H) G(E,F,I)
H(F,I) I(G,H).

### 3.2 Convergencia y enrutamiento en LSR

Partiendo de nodos que solo conocen a sus vecinos, la red converge y el tráfico
sigue el camino de menor costo:

```
[I] MESSAGE from A (path A>C>F>G): hola-multi-hop
```

El nodo A nunca fue informado de la existencia de I; aprendió la ruta
exclusivamente a partir de los anuncios difundidos.

### 3.3 Entrega única bajo flooding

Sobre la misma topología, que contiene ciclos, un mensaje enviado en modo
flooding se entrega exactamente una vez:

```
[I] MESSAGE from A (path A>B>D>E>G): flood-test
```

La prueba de integración `TestFloodingDeliversExactlyOnceDespiteACycle` verifica
además que no llegue una segunda copia dentro de una ventana de espera.

### 3.4 Reacción ante la caída de un router

Se detuvo el contenedor del nodo F, que formaba parte de la ruta activa hacia I,
y se reenvió el mensaje tras la convergencia:

| Momento | Ruta observada | Siguiente salto desde A |
| --- | --- | --- |
| Antes de la falla | `A>C>F>H` | C |
| Después de la falla | `A>B>D>E>G` | B |

El nodo A registró la retirada de los enlaces hacia F por parte de C, D, G y H,
y posteriormente la expiración del anuncio del propio F:

```
[A] topology update: C is linked to [A B]
[A] topology update: D is linked to [B E]
[A] topology update: G is linked to [E I]
[A] link-state packet of F expired, removing it from the topology
```

Al reiniciar el contenedor, F fue reincorporado a la topología.

### 3.5 Tráfico de control

Medición del número de secuencia alcanzado por los anuncios, como indicador
directo del volumen de señalización:

| Versión | Tiempo de ejecución | Secuencia máxima observada |
| --- | --- | --- |
| Antes de la corrección | 60 s | 85 |
| Después de la corrección | 90 s | 12 |

Con un refresco periódico de 8 segundos, 90 segundos de ejecución corresponden a
aproximadamente 11 anuncios por nodo. Es decir, tras la corrección el tráfico de
control equivale al refresco diseñado y nada más.

Estado de la base de datos de enlaces del nodo A tras la corrección:

```
B  seq=11   A(0.13) C(0.12) D(0.12)
C  seq=11   A(0.14) B(0.10) F(0.09)
D  seq=11   B(0.11) E(0.14) F(0.13)
E  seq=10   D(0.11) G(0.11)
F  seq=12   C(0.11) D(0.12) G(0.12) H(0.12)
G  seq=11   E(0.11) F(0.11) I(0.11)
H  seq=10   F(0.17) I(0.15)
I  seq=10   G(0.16) H(0.12)
```

### 3.6 Interconexión en clase

> _Sección pendiente de completar tras la sesión presencial._
>
> Documentar: direcciones IP asignadas a cada grupo, topología acordada,
> ajustes al protocolo negociados entre equipos, mensajes intercambiados con
> nodos de otras implementaciones, incompatibilidades encontradas y cómo se
> resolvieron, y tiempo de convergencia observado en la red completa.

---

## 4. Discusión

### 4.1 Comparación entre los algoritmos

| | Información requerida | Tabla | Tráfico de datos | Reacción a fallas |
| --- | --- | --- | --- | --- |
| Dijkstra | Topología completa | Calculada al arrancar | Mínimo, una copia por camino | Poda vecinos caídos |
| Flooding | Solo vecinos | Ninguna | Alto, crece con las aristas | Deja de usar el enlace mudo |
| LSR | Vecinos, luego topología aprendida | Recalculada ante cambios | Mínimo en datos, moderado en control | Reanuncia, envejece y recalcula |

Flooding es el único que funciona sin ninguna información previa, y por eso es
el mecanismo de arranque de LSR: antes de conocer la topología no existe forma
de dirigir un anuncio hacia un destino específico. Su costo es que el tráfico de
datos crece con el número de aristas, lo que lo hace inviable como algoritmo de
red general.

Dijkstra produce el enrutamiento óptimo con el mínimo tráfico, pero exige que
alguien le entregue la topología completa, lo cual no es realista en una red que
cambia.

LSR resuelve esa tensión: usa flooding solo para el tráfico de control, que es
acotado y periódico, y usa Dijkstra sobre la topología resultante para el
tráfico de datos, que es el volumen realmente significativo.

### 4.2 Sobre la modularidad

La decisión de implementar Dijkstra y Flooding como funciones puras tuvo un
efecto verificable: las pruebas unitarias de ambos algoritmos no requieren
sockets, procesos ni temporizadores, y las pruebas de LSR ejercitan exactamente
el mismo código que se ejecuta en producción. Si se hubieran escrito como dos
programas separados con lógica duplicada, cualquier corrección en uno habría
tenido que replicarse manualmente en el otro.

### 4.3 Sobre la medición de costos

Medir el costo de un enlace con el tiempo real de ida y vuelta es más realista
que asignar pesos fijos, pero introduce el problema descrito en la sección 2.6:
la señalización pasa a depender del ruido de medición. Esta es una diferencia
importante entre implementar un algoritmo sobre un grafo estático y operarlo
sobre una red real, y no se manifestó en las pruebas unitarias sino únicamente
al ejecutar la red completa.

### 4.4 Limitaciones

- El TTL por defecto es de 8 saltos. En una red mayor que la del laboratorio
  habría que aumentarlo.
- Los costos se miden en milisegundos de retardo; no se considera ancho de banda
  ni pérdida de paquetes.
- No se implementó autenticación de los anuncios: un nodo malicioso podría
  anunciar enlaces inexistentes y atraer tráfico.
- La detección de caída depende de que el vecino deje de responder. Un enlace
  que responde pero descarta datos no se detecta.

---

## 5. Conclusiones

1. Los tres algoritmos se implementaron y verificaron sobre una red de nueve
   nodos, tanto en procesos locales como en contenedores independientes.
2. Tratar Dijkstra y Flooding como funciones puras, y no como programas
   separados, permitió que LSR los reutilizara literalmente. Existe una sola
   implementación de cada algoritmo en el proyecto.
3. La terminación del flooding depende de la detección de duplicados, no del
   TTL. El TTL es una protección de último recurso frente a implementaciones
   externas incorrectas.
4. LSR converge desde el desconocimiento total de la topología y se recupera de
   la caída de un router intermedio sin intervención manual.
5. El problema de mayor impacto encontrado no fue algorítmico sino operativo: el
   ruido de las mediciones de costo generaba una tormenta de señalización. Solo
   se hizo evidente al ejecutar la red completa, lo que muestra que las pruebas
   unitarias son necesarias pero no suficientes para un sistema distribuido.

---

## 6. Comentarios

_(Espacio para observaciones del grupo sobre la práctica, dificultades
encontradas y sugerencias.)_

---

## 7. Referencias

1. Tanenbaum, A. S., & Wetherall, D. J. (2011). *Computer Networks* (5.ª ed.).
   Prentice Hall. Capítulo 5: The Network Layer.
2. Kurose, J. F., & Ross, K. W. (2016). *Computer Networking: A Top-Down
   Approach* (7.ª ed.). Pearson. Capítulo 5: The Network Layer — Control Plane.
3. Dijkstra, E. W. (1959). A note on two problems in connexion with graphs.
   *Numerische Mathematik*, 1(1), 269–271.
4. Moy, J. (1998). *OSPF Version 2* (RFC 2328). Internet Engineering Task Force.
   https://www.rfc-editor.org/rfc/rfc2328
5. The Go Authors. *The Go Programming Language Specification*.
   https://go.dev/ref/spec
