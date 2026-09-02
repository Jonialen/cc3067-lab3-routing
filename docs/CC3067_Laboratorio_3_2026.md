# CC3067 - Laboratorio 3 - 2026

## 1 Antecedentes
Conociendo a dónde enviar los mensajes para cualquier router se vuelve trivial el envío de mensajes. Únicamente es necesario conocer el destino final y se reenvía al vecino que puede proveer la mejor ruta al destino. Toda esa información es almacenada en las tablas de enrutamiento.

No obstante, con el dinamismo con el que se espera que pueda funcionar el Internet es necesario que dichas tablas puedan actualizarse y acomodarse a cambios en la infraestructura. Los algoritmos con los que se actualizan estas tablas son conocidos como algoritmos de enrutamiento.

## 2 Objetivos
* Conocer los algoritmos de enrutamiento utilizados en las implementaciones actuales de Internet.
* Comprender cómo funcionan las tablas de enrutamiento.
* Implementar los algoritmos de enrutamiento y probarlos en una red.
* Analizar el funcionamiento de los algoritmos de enrutamiento.

## 3 Desarrollo
Los algoritmos de enrutamiento funcionan sobre nodos interconectados entre sí, donde cada nodo conoce únicamente cuáles son los vecinos que tiene. Dicha información inicial será proporcionada para cada nodo.

A partir de ello, se levantarán varias instancias (nodos) de las implementaciones de los algoritmos que se describen más adelante, para proceder a enviar mensajes y simular una red. El objetivo es implementar los algoritmos, realizar pruebas en conjunto con los integrantes del grupo para validar el funcionamiento y posteriormente realizar una prueba general con los demás miembros de la clase.

Para tales efectos se conformarán grupos de 4 integrantes donde se implementarán los algoritmos según descrito más adelante.

*[Imagen 1: Mapa de conexiones entre nodos]*

En esta propuesta cada uno de los nodos corresponde a un cliente el cuál puede enviar o recibir mensajes para conformar su tabla de enrutamiento y posteriormente enviar un paquete de un nodo origen a un nodo destino utilizando dicha tabla. Cada nodo corresponde a un proceso independiente que se comunica vía sockets.

### 3.1 Implementación de algoritmos
Los algoritmos para implementarse son:
1. Dijkstra
2. Flooding
3. Link state routing

Las implementaciones deben de estar debidamente identificadas y publicadas en un repositorio privado (el día de la entrega lo hacen público), junto con sus requerimientos para su uso e instalación. Las implementaciones idealmente deben de poder ejecutarse en distintas plataformas de forma sencilla (makefiles, python venv, npm run, .jar).

Dependiendo del algoritmo, tendremos distintos inputs requeridos por cada uno para funcionar adecuadamente. Lo que requiere cada algoritmo incluye:
* **Dijkstra**: Topología (nodos, aristas)
* **Flooding**: conocimiento de sus vecinos solamente.
* **Link State Routing**: Las Tablas de los demás Nodos (de ella se deriva la Topología)

Nótese que Dijkstra y Flooding se usan en LSR, por lo que deben manejar alta modularidad en sus Clases y archivos. Además, sus programas deben ser capaces de correr Flooding y Dijkstra como el algoritmo de la red, independientemente de su uso en LSR (o sea, levantar los nodos en modo "flooding", y enviarnos mensajes así, o levantarlo en modo Dijkstra y probarlo así aunque sepamos que es estático).

### 3.2 Conexión y Pruebas de los algoritmos
Para probar los algoritmos implementados utilizaremos una conexión de red provista por el profesor el día de la presentación en clase. Usaremos una red para realizar las pruebas.

Como toda Red, debemos definir protocolos y formatos estándares para comunicarnos. La base es la siguiente, pudiendo agregar elementos si así lo consideran (ojo, deben ponerse de acuerdo entre todos los grupos para definir el protocolo. Esto significa que debería haber interoperabilidad entre códigos de distintos grupos). La estructura será tipo JSON:

```json
{
  "proto": "dijkstra | flooding | Isr|dvr|...",
  "type": "message | echo | info | hello|...",
  "from": "IP ORIGEN",
  "to": "IP DESTINO",
  "ttl": 5,
  "headers": [{"opcional": "foo"}, {"alguna_optimizacion_suya": "bar"}],
  "payload": "el contenido del paquete dependiendo de su tipo. Si es un mensaje de usuario seria algo como este texto y se debe forward a destino o print si somos nosotros destino. Si es un mensaje con info de tablas o enlace, aca iría ese contenido que cada nodo puede extraer del payload y utilizar para sus cálculos y tablas."
}
```

Este ejemplo puede servir como base para implementar el protocolo, es importante que exista comunicación entre los equipos para implementar un protocolo común.

### 3.3 Otros detalles para las pruebas y conexiones
El día de la entrega probaremos los algoritmos en clase. Estaremos interconectando los distintos grupos a través de una red, a través de un medio inalámbrico, cada grupo obtendrá una dirección IP al conectarse, que debe comunicarse a los demás grupos para definir las adyacencias en base a la topología de ejemplo.

Se estará probando en clase LSR (Dijkstra y Flooding son utilizados en LSR, Dijkstra no se probará directamente).

Adicional, se establecerán mapas de conexiones entre nodos similar al de la Imagen 1. Se estará utilizando una topología similar a la del ejemplo, la cual deben utilizar para configurar sus nodos y solamente para eso. Cualquiera de los nodos debe de tener la capacidad de enviar y/o recibir un mensaje.

Al iniciar un nodo, este obtendrá la configuración y procederá a descubrir a sus vecinos. El nodo tendrá dos procesos/hilos en simultáneo: el forwarding y el routing. Todo debe correr en paralelo/asíncrono mediante el uso de hilos, procesos etc. Cada servicio se encarga de cosas específicas, como por ejemplo:

* **Forwarding**
  * *Manejo de paquetes entrantes*
    * Paquetes de Datos: forward o print si es para nosotros
    * Paquete de Info: dependiendo del algoritmo, como el Vector de Distancias o el LSP. Recibirlos y pasarlos al proceso de Ruteo.
    * Paquete de Hello/Ping: Descubrimiento de nodos y medición de distancia hacia ellos.
  * *Manejo de paquetes salientes*
    * Forward messages
    * Forward Flooding
    * Forward DV/LSP/INFO
    * Send Hello/Ping
  * Confirmaciones de recepción, etc.
* **Routing**
  * Inicializar la Tabla de Ruteo
  * Armar paquetes de Info
  * Consultar paquetes de Info y nuevos nodos entrantes
  * Utilizar paquetes de info para resolver y actualizar las tablas según cada algoritmo hace.

Siguiendo el formato establecido, deberán enviarse y definir distintos tipos de mensajes para el funcionamiento de los algoritmos. Se sugiere un paquete tipo HELLO/PING, para medir delays entre nodos e inicializar nodos. Se sugiere un paquete DATA/MESSAGE, el cual contenga data de usuario (mensajes) en su payload. Se sugiere un paquete TABLE/INFO, el cual contenga información de tablas, ruteo, vecinos, etc. Pueden agregar otros tipos si así desean y les sirve.

El objetivo es lograr que los algoritmos se estabilicen y los mensajes pasen por los nodos que corresponden a la ruta óptima, así como el poder responder o adaptarse a nuevos nodos, nodos caídos, etc.

## 4 Rúbrica de evaluación

| Elemento | Ponderación | Sub-elemento | Porcentaje |
| :--- | :---: | :--- | :---: |
| **Código** | **75%** | Documentación, orden, comentarios, limpieza, legibilidad/funcionalidad balanceada, etc. | 5% |
| | | Implementación de los Algoritmos, de forma eficiente y optimizada. | 35% |
| | | Interconexión de los algoritmos en clase. | 35% |
| **Reporte Escrito** | **25%** | Encabezado, Ortografía, Formato Adecuado, Descripción de la Práctica | 2.5% |
| | | Descripción de los Algoritmos Utilizados y su Implementación | 10% |
| | | Resultados | 5% |
| | | Discusión | 5% |
| | | Conclusiones + Comentarios + Referencias | 2.5% |

**\*\* Una inasistencia injustificada anula la nota del laboratorio.\*\***

### Entregar en Canvas:
1. Archivo .pdf con su reporte en grupo
2. Código utilizado para el Laboratorio, en un rar si es necesario
3. Link a su repositorio, el cual es privado hasta antes de la entrega

**Fecha de entrega:** Jueves 3 de septiembre, durante la clase.
