// SPDX-License-Identifier: MIT
pragma solidity 0.7.6;

import "./AccessControllerInterface.sol";
import "./AggregatorV2V3Interface.sol";
import "./AggregatorValidatorInterface.sol";
import "./LinkTokenInterface.sol";
import "./Owned.sol";
import "./OffchainAggregatorBilling.sol";
import "./TypeAndVersionInterface.sol";

/**
  * @notice 离链报告协议的在链验证

  * @dev 有关其操作的详细信息，请参见离链报告协议设计
  * @dev 文档，该协议将此合约简称为“contract”。
*/
contract OffchainAggregator is Owned, OffchainAggregatorBilling, AggregatorV2V3Interface, TypeAndVersionInterface {

  // 最大uint32
  uint256 constant private maxUint32 = (1 << 32) - 1;

  // 存储热路径中使用的这些字段在HotVars变量中可以将它们的检索减少为单个SLOAD。如果添加了任何其他字段，请确保结构的存储仍然最多占用32个字节。
  struct HotVars {
    // 对2次预图像攻击提供128位安全性，但只对碰撞提供64位安全性。这是可以接受的，因为恶意所有者更容易破坏协议而不是找到哈希碰撞。
    bytes16 latestConfigDigest;
    uint40 latestEpochAndRound; // 32个最重要的位用于epoch，8个最不重要的位用于round
    // 在参与协议中假设的当前边界上存储故障/不诚实的预言机的数量，该值在设计中被称为f
    uint8 threshold;
    // Chainlink聚合器向使用者暴露一个roundId。离链报告协议在任何地方都不使用此id。我们在进行新的传输时递增它，以为连续的报告提供连续的ids。
    uint32 latestAggregatorRoundId;
  }
  HotVars internal s_hotVars;

  // 传输记录了来自传输事务的中位数答案和时间戳
  struct Transmission {
    int192 answer; // 192位应该足够
    uint64 timestamp;
  }
  mapping(uint32 /* 聚合器轮次ID */ => Transmission) internal s_transmissions;

  // 每次发布新配置时递增。此计数包含在配置摘要中，以防止重播攻击。
  uint32 internal s_configCount;
  uint32 internal s_latestConfigBlockNumber; // 方便离链系统从日志中提取配置。

  // 系统允许的最低答案，用于响应传输的报告
  int192 immutable public minAnswer;
  // 系统允许的最高答案，用于响应传输的报告
  int192 immutable public maxAnswer;

  /*
   * @param _maximumGasPrice 负责人将获得报酬的最高燃气价格
   * @param _reasonableGasPrice 传输者将获得报酬的燃气价格
   * @param _microLinkPerEth 每ETH的燃气费用补偿，以1e-6LINK为单位
   * @param _linkGweiPerObservation 奖励预言机为成功传输的报告提供的观察的LINK奖励，以1e-9LINK为单位
   * @param _linkGweiPerTransmission 奖励成功报告的传输者的LINK奖励，以1e-9LINK为单位
   * @param _link LINK合约的地址
   * @param _minAnswer 报告中位数允许的最低答案
   * @param _maxAnswer 报告中位数允许的最高答案
   * @param _billingAccessController 账单管理功能的访问控制器
   * @param _requesterAccessController 请求新轮次的访问控制器
   * @param _decimals 答案以固定点格式存储，精度为多少位
   * @param _description 与此合约的答案相关的可观察事物的简短人类可读描述
   */
  constructor(
    uint32 _maximumGasPrice,
    uint32 _reasonableGasPrice,
    uint32 _microLinkPerEth,
    uint32 _linkGweiPerObservation,
    uint32 _linkGweiPerTransmission,
    LinkTokenInterface _link,
    int192 _minAnswer,
    int192 _maxAnswer,
    AccessControllerInterface _billingAccessController,
    AccessControllerInterface _requesterAccessController,
    uint8 _decimals,
    string memory _description
  )
    OffchainAggregatorBilling(_maximumGasPrice, _reasonableGasPrice, _microLinkPerEth,
      _linkGweiPerObservation, _linkGweiPerTransmission, _link,
      _billingAccessController
    )
  {
    decimals = _decimals;
    s_description = _description;
    setRequesterAccessController(_requesterAccessController);
    setValidatorConfig(AggregatorValidatorInterface(0x0), 0);
    minAnswer = _minAnswer;
    maxAnswer = _maxAnswer;
  }

  /*
   * 版本控制
   */
  function typeAndVersion()
    external
    override
    pure
    virtual
    returns (string memory)
  {
    return "OffchainAggregator 4.0.0";
  }

  /*
   * 配置逻辑
   */

  /**
   * @notice 触发离链报告协议的新运行
   * @param previousConfigBlockNumber 设置前配置的块，以简化历史分析
   * @param configCount 在该合约的所有配置设置的生命周期中，此配置设置的序号
   * @param signers ith元素是第i个预言机用于签署报告的地址
   * @param transmitters ith元素是第i个预言机用于通过传输方法传输报告的地址
   * @param threshold 协议在正确工作时可以容忍的最大故障/不诚实的预言机数量
   * @param encodedConfigVersion 用于“encoded”参数的序列化格式的版本
   * @param encoded 用于配置预言机离链操作的序列化数据
   */
  event ConfigSet(
    uint32 previousConfigBlockNumber,
    uint64 configCount,
    address[] signers,
    address[] transmitters,
    uint8 threshold,
    uint64 encodedConfigVersion,
    bytes encoded
  );

  // 如果配置参数无效，则还原事务
  modifier checkConfigValid (
    uint256 _numSigners, uint256 _numTransmitters, uint256 _threshold
  ) {
    require(_numSigners <= maxNumOracles, "too many signers");
    require(_threshold > 0, "threshold must be positive");
    require(
      _numSigners == _numTransmitters,
      "oracle addresses out of registration"
    );
    require(_numSigners > 3*_threshold, "faulty-oracle threshold too high");
    _;
  }

  /**
   * @notice 设置离链报告协议配置，包括参与预言机
   * @param _signers 预言机用于签署报告的地址
   * @param _transmitters 预言机用于传输报告的地址
   * @param _threshold 系统可以容忍的最大故障预言机数量
   * @param _encodedConfigVersion offchainEncoding模式的版本号
   * @param _encoded offchain配置的序列化数据
   */
  function setConfig(
    address[] calldata _signers,
    address[] calldata _transmitters,
    uint8 _threshold,
    uint64 _encodedConfigVersion,
    bytes calldata _encoded
  )
    external
    checkConfigValid(_signers.length, _transmitters.length, _threshold)
    onlyOwner()
  {
    while (s_signers.length != 0) { // 删除任何旧的签署者/传输者地址
      uint lastIdx = s_signers.length - 1;
      address signer = s_signers[lastIdx];
      address transmitter = s_transmitters[lastIdx];
      payOracle(transmitter);
      delete s_oracles[signer];
      delete s_oracles[transmitter];
      s_signers.pop();
      s_transmitters.pop();
    }

    for (uint i = 0; i < _signers.length; i++) { // 添加新的签署者/传输者地址
      require(
        s_oracles[_signers[i]].role == Role.Unset,
        "repeated signer address"
      );
      s_oracles[_signers[i]] = Oracle(uint8(i), Role.Signer);
      require(s_payees[_transmitters[i]] != address(0), "payee must be set");
      require(
        s_oracles[_transmitters[i]].role == Role.Unset,
        "repeated transmitter address"
      );
      s_oracles[_transmitters[i]] = Oracle(uint8(i), Role.Transmitter);
      s_signers.push(_signers[i]);
      s_transmitters.push(_transmitters[i]);
    }
    s_hotVars.threshold = _threshold;
    uint32 previousConfigBlockNumber = s_latestConfigBlockNumber;
    s_latestConfigBlockNumber = uint32(block.number);
    s_configCount += 1;
    uint64 configCount = s_configCount;
    {
      s_hotVars.latestConfigDigest = configDigestFromConfigData(
        address(this),
        configCount,
        _signers,
        _transmitters,
        _threshold,
        _encodedConfigVersion,
        _encoded
      );
      s_hotVars.latestEpochAndRound = 0;
    }
    emit ConfigSet(
      previousConfigBlockNumber,
      configCount,
      _signers,
      _transmitters,
      _threshold,
      _encodedConfigVersion,
      _encoded
    );
  }

  function configDigestFromConfigData(
    address _contractAddress,
    uint64 _configCount,
    address[] calldata _signers,
    address[] calldata _transmitters,
    uint8 _threshold,
    uint64 _encodedConfigVersion,
    bytes calldata _encodedConfig
  ) internal pure returns (bytes16) {
    return bytes16(keccak256(abi.encode(_contractAddress, _configCount,
      _signers, _transmitters, _threshold, _encodedConfigVersion, _encodedConfig
    )));
  }

  /**
   * @notice 当前离链报告协议配置的信息

   * @return configCount 当前配置的序号，是此合约到目前为止应用的所有配置中的序号
   * @return blockNumber 设置此配置的块
   * @return configDigest 当前配置的域分离标记（参见configDigestFromConfigData）
   */
  function latestConfigDetails()
    external
    view
    returns (
      uint32 configCount,
      uint32 blockNumber,
      bytes16 configDigest
    )
  {
    return (s_configCount, s_latestConfigBlockNumber, s_hotVars.latestConfigDigest);
  }

  /**
   * @return 允许向此合约传输报告的地址列表

   * @dev 列表将与在setConfig期间指定的顺序匹配传输者
   */
  function transmitters()
    external
    view
    returns(address[] memory)
  {
      return s_transmitters;
  }

  /*
   * 在链验证逻辑
   */

  // 验证者的配置
  struct ValidatorConfig {
    AggregatorValidatorInterface validator;
    uint32 gasLimit;
  }
  ValidatorConfig private s_validatorConfig;

  /**
   * @notice 表明已设置验证者配置
   * @param previousValidator 先前的验证者合约
   * @param previousGasLimit 先前的验证调用的燃气限制
   * @param currentValidator 当前的验证者合约
   * @param currentGasLimit 当前验证调用的燃气限制
   */
  event ValidatorConfigSet(
    AggregatorValidatorInterface indexed previousValidator,
    uint32 previousGasLimit,
    AggregatorValidatorInterface indexed currentValidator,
    uint32 currentGasLimit
  );

  /**
   * @notice 验证者配置
   * @return validator 验证者合约
   * @return gasLimit 验证调用的燃气限制
   */
  function validatorConfig()
    external
    view
    returns (AggregatorValidatorInterface validator, uint32 gasLimit)
  {
    ValidatorConfig memory vc = s_validatorConfig;
    return (vc.validator, vc.gasLimit);
  }

  /**
   * @notice 设置验证者配置
   * @dev 将_newValidator设置为0x0以禁用验证调用
   * @param _newValidator 新验证者合约的地址
   * @param _newGasLimit 验证调用的新燃气限制
   */
  function setValidatorConfig(AggregatorValidatorInterface _newValidator, uint32 _newGasLimit)
    public
    onlyOwner()
  {
    ValidatorConfig memory previous = s_validatorConfig;

    if (previous.validator != _newValidator || previous.gasLimit != _newGasLimit) {
      s_validatorConfig = ValidatorConfig({
        validator: _newValidator,
        gasLimit: _newGasLimit
      });

      emit ValidatorConfigSet(previous.validator, previous.gasLimit, _newValidator, _newGasLimit);
    }
  }

  function validateAnswer(
    uint32 _aggregatorRoundId,
    int256 _answer
  )
    private
  {
    ValidatorConfig memory vc = s_validatorConfig;

    if (address(vc.validator) == address(0)) {
      return;
    }

    uint32 prevAggregatorRoundId = _aggregatorRoundId - 1;
    int256 prevAggregatorRoundAnswer = s_transmissions[prevAggregatorRoundId].answer;
    require(
      callWithExactGasEvenIfTargetIsNoContract(
        vc.gasLimit,
        address(vc.validator),
        abi.encodeWithSignature(
          "validate(uint256,int256,uint256,int256)",
          uint256(prevAggregatorRoundId),
          prevAggregatorRoundAnswer,
          uint256(_aggregatorRoundId),
          _answer
        )
      ),
      "insufficient gas"
    );
  }

  uint256 private constant CALL_WITH_EXACT_GAS_CUSHION = 5_000;

  /**
   * @dev 使用恰好为gasAmount的燃气和数据作为calldata调用目标地址，否则将恢复，如果至少有gasAmount的燃气可用。 (up to gas-block limit)
   */
  function callWithExactGasEvenIfTargetIsNoContract(
    uint256 _gasAmount,
    address _target,
    bytes memory _data
  )
    private
    returns (bool sufficientGas)
  {
    // solhint-disable-next-line no-inline-assembly
    assembly {
      let g := gas()
      // 计算 g -= CALL_WITH_EXACT_GAS_CUSHION，并检查下溢。我们需要这个缓冲区，因为在调用gas后面的逻辑时，也会产生燃气，我们无法准确地计算它的成本。因此，缓冲区是这个逻辑成本的保守上限。
      if iszero(lt(g, CALL_WITH_EXACT_GAS_CUSHION)) {
        g := sub(g, CALL_WITH_EXACT_GAS_CUSHION)
        // 如果 g - g//64 <= _gasAmount，则我们没有足够的燃气。(我们减去g//64是因为EIP-150。)
        if gt(sub(g, div(g, 64)), _gasAmount) {
          // 调用并忽略成功/返回数据。请注意，我们没有检查合约是否实际上存在于_target地址。
          pop(call(_gasAmount, _target, 0, add(_data, 0x20), mload(_data), 0, 0))
          sufficientGas := true
        }
      }
    }
  }

  /*
   * 请求新轮次逻辑
   */

  AccessControllerInterface internal s_requesterAccessController;

  /**
   * @notice 当新请求者访问控制器合约设置时发出
   * @param old 当前设置之前的地址
   * @param current 新的访问控制器合约地址
   */
  event RequesterAccessControllerSet(AccessControllerInterface old, AccessControllerInterface current);

  /**
   * @notice 在立即请求新轮次时发出
   * @param requester 请求者地址
   * @param configDigest 最新传输的configDigest
   * @param epoch 最新传输的epoch
   * @param round 最新传输的round
   */
  event RoundRequested(address indexed requester, bytes16 configDigest, uint32 epoch, uint8 round);

  /**
   * @notice 请求者访问控制器合约地址
   * @return requester访问控制器地址
   */
  function requesterAccessController()
    external
    view
    returns (AccessControllerInterface)
  {
    return s_requesterAccessController;
  }

  /**
   * @notice 设置请求者访问控制器
   * @param _requesterAccessController 指定新请求者访问控制器的地址
   */
  function setRequesterAccessController(AccessControllerInterface _requesterAccessController)
    public
    onlyOwner()
  {
    AccessControllerInterface oldController = s_requesterAccessController;
    if (_requesterAccessController != oldController) {
      s_requesterAccessController = AccessControllerInterface(_requesterAccessController);
      emit RequesterAccessControllerSet(oldController, _requesterAccessController);
    }
  }

  /**
   * @notice 立即请求新轮次
   * @return 下一轮的聚合器轮次ID。注意：在调用requestNewRound()之前，该轮的报告可能已经被传输（但尚未被挖掘）。无法保证请求和聚合器RoundId之间的因果关系。
   */
  function requestNewRound() external returns (uint80) {
    require(msg.sender == owner || s_requesterAccessController.hasAccess(msg.sender, msg.data),
      "Only owner&requester can call");

    HotVars memory hotVars = s_hotVars;

    emit RoundRequested(
      msg.sender,
      hotVars.latestConfigDigest,
      uint32(s_hotVars.latestEpochAndRound >> 8),
      uint8(s_hotVars.latestEpochAndRound)
    );
    return hotVars.latestAggregatorRoundId + 1;
  }

  /*
   * 传输逻辑
   */

  /**
   * @notice 表明已传输新报告
   * @param aggregatorRoundId 分配给此报告的轮次
   * @param answer 与此报告一起传输的中位数答案
   * @param transmitter 传输报告的地址
   * @param observations 与此报告一起传输的观察
   * @param rawReportContext 签名回放预防域分离标记
   */
  event NewTransmission(
    uint32 indexed aggregatorRoundId,
    int192 answer,
    address transmitter,
    int192[] observations,
    bytes observers,
    bytes32 rawReportContext
  );

  // 解码报告用于检查Solidity和Go代码是否使用相同的格式。请参阅TestOffchainAggregator.testDecodeReport和TestReportParsing
  function decodeReport(bytes memory _report)
    internal
    pure
    returns (
      bytes32 rawReportContext,
      bytes32 rawObservers,
      int192[] memory observations
    )
  {
    (rawReportContext, rawObservers, observations) = abi.decode(_report,
      (bytes32, bytes32, int192[]));
  }

  // 在传输中减轻堆栈压力
  struct ReportData {
    HotVars hotVars; // 仅从存储中读取一次
    bytes observers; // ith元素是第i个观察者的索引
    int192[] observations; // ith元素是第i个观察的值
    bytes vs; // jth元素是第j个签名的v分量
    bytes32 rawReportContext;
  }

  /*
   * @notice 最新报告的详细信息

   * @return configDigest 最新报告的域分隔标记
   * @return epoch 生成最新报告的时期
   * @return round OCR中生成最新报告的轮次
   * @return latestAnswer 最新报告的中位数值
   * @return latestTimestamp 传输最新报告时的时间戳
   */
  function latestTransmissionDetails()
    external
    view
    returns (
      bytes16 configDigest,
      uint32 epoch,
      uint8 round,
      int192 latestAnswer,
      uint64 latestTimestamp
    )
  {
    require(msg.sender == tx.origin, "Only callable by EOA");
    return (
      s_hotVars.latestConfigDigest,
      uint32(s_hotVars.latestEpochAndRound >> 8),
      uint8(s_hotVars.latestEpochAndRound),
      s_transmissions[s_hotVars.latestAggregatorRoundId].answer,
      s_transmissions[s_hotVars.latestAggregatorRoundId].timestamp
    );
  }

  // 传输消息数据的常量长度组件。
  // 有关示例推理的详细信息，请参见“如果我们想叫Sam”的示例
  // https://solidity.readthedocs.io/en/v0.7.2/abi-spec.html
  uint16 private constant TRANSMIT_MSGDATA_CONSTANT_LENGTH_COMPONENT =
    4 + // 功能选择器
    32 + // abiencoded _report值的起始位置的单词
    32 + // abiencoded _rs起始位置的单词
    32 + // abiencoded _ss起始位置的单词
    32 + // _rawVs的值
    32 + // abiencoded _report长度的单词
    32 + // abiencoded _rs长度的单词
    32 + // abiencoded _ss长度的单词
    0; // 占位符

  function expectedMsgDataLength(
    bytes calldata _report, bytes32[] calldata _rs, bytes32[] calldata _ss
  ) private pure returns (uint256 length)
  {
    // calldata永远不会足够大而导致溢出
    return uint256(TRANSMIT_MSGDATA_CONSTANT_LENGTH_COMPONENT) +
      _report.length + // _report的一个字节纯输入
      _rs.length * 32 + // _rs中每个条目的32个字节
      _ss.length * 32 + // _ss中每个条目的32个字节
      0; // 占位符
  }

  /**
   * @notice 用于将新报告发布到合约的调用传输
   * @param _report 序列化报告，签名对其进行签名。请参见下面的解析代码以了解格式。观察者组件的第i个元素必须是第i个签名的地址在s_signers中的索引
   * @param _rs 第i个元素是在报告上的第i个签名的R成分。最多可以有maxNumOracles个条目
   * @param _ss 第i个元素是在报告上的第i个签名的S成分。最多可以有maxNumOracles个条目
   * @param _rawVs 第i个元素是第i个签名的V成分
   */
  function transmit(
    // 如果更改了这些参数，则需要相应地更改expectedMsgDataLength和/或TRANSMIT_MSGDATA_CONSTANT_LENGTH_COMPONENT
    bytes calldata _report,
    bytes32[] calldata _rs, bytes32[] calldata _ss, bytes32 _rawVs // 签名
  )
    external
  {
    uint256 initialGas = gasleft(); // 此行必须最先执行
    // 确保传输消息长度与输入匹配。否则，传输者可以追加任意长（最高为gas-block限制）的0字节字符串，我们将以16 gas/byte的价格补偿它，但是只会让传输者以4 gas/byte的价格支付。 （黄皮书的附录G第25页的Appendix G，以及EIP 2028的G_txdatanonzero和G_txdatanonzero）。
    // 这可能导致3600万gas的补偿利润，给出了3MB的零尾。
    require(msg.data.length == expectedMsgDataLength(_report, _rs, _ss),
      "transmit message too long");
    ReportData memory r; // 减轻堆栈压力
    {
      r.hotVars = s_hotVars; // 从存储中缓存读取

      bytes32 rawObservers;
      (r.rawReportContext, rawObservers, r.observations) = abi.decode(
        _report, (bytes32, bytes32, int192[])
      );

      // rawReportContext包含：
      // 11个字节的零填充
      // 16字节的configDigest
      // 4个字节的epoch
      // 1个字节的round

      bytes16 configDigest = bytes16(r.rawReportContext << 88);
      require(
        r.hotVars.latestConfigDigest == configDigest,
        "configDigest mismatch"
      );

      uint40 epochAndRound = uint40(uint256(r.rawReportContext));

      // 直接数字比较在这里起作用，因为
      //
      //   ((e,r) <= (e',r'))则意味着（epochAndRound <= epochAndRound'）
      //
      // 因为字母顺序意味着e <= e'，如果e = e'，那么r<=r'，所以e*256+r <= e'*256+r'，因为r、r' <256
      require(r.hotVars.latestEpochAndRound < epochAndRound, "stale report");

      require(_rs.length > r.hotVars.threshold, "not enough signatures");
      require(_rs.length <= maxNumOracles, "too many signatures");
      require(_ss.length == _rs.length, "signatures out of registration");
      require(r.observations.length <= maxNumOracles,
              "num observations out of bounds");
      require(r.observations.length > 2 * r.hotVars.threshold,
              "too few values to trust median");

      // 将bytes32 _rawVs中的签名奇偶性复制到bytes r.v
      r.vs = new bytes(_rs.length);
      for (uint8 i = 0; i < _rs.length; i++) {
        r.vs[i] = _rawVs[i];
      }

      // 将bytes32 rawObservers中的观察者标识复制到bytes r.observers
      r.observers = new bytes(r.observations.length);
      bool[maxNumOracles] memory seen;
      for (uint8 i = 0; i < r.observations.length; i++) {
        uint8 observerIdx = uint8(rawObservers[i]);
        require(!seen[observerIdx], "observer index repeated");
        seen[observerIdx] = true;
        r.observers[i] = rawObservers[i];
      }

      Oracle memory transmitter = s_oracles[msg.sender];
      require( // 检查发送者是否有权限报告
        transmitter.role == Role.Transmitter &&
        msg.sender == s_transmitters[transmitter.index],
        "unauthorized transmitter"
      );
      // 记录epochAndRound，以便我们不必在传输中传递本地变量。如果之后发生错误，则更改将被还原。
      r.hotVars.latestEpochAndRound = epochAndRound;
    }

    { // 验证附加到报告的签名
      bytes32 h = keccak256(_report);
      bool[maxNumOracles] memory signed;

      Oracle memory o;
      for (uint i = 0; i < _rs.length; i++) {
        address signer = ecrecover(h, uint8(r.vs[i])+27, _rs[i], _ss[i]);
        o = s_oracles[signer];
        require(o.role == Role.Signer, "address not authorized to sign");
        require(!signed[o.index], "non-unique signature");
        signed[o.index] = true;
      }
    }

    { // 检查报告内容，并记录结果
      for (uint i = 0; i < r.observations.length - 1; i++) {
        bool inOrder = r.observations[i] <= r.observations[i+1];
        require(inOrder, "observations not sorted");
      }

      int192 median = r.observations[r.observations.length/2];
      require(minAnswer <= median && median <= maxAnswer, "median is out of min-max range");
      r.hotVars.latestAggregatorRoundId++;
      s_transmissions[r.hotVars.latestAggregatorRoundId] =
        Transmission(median, uint64(block.timestamp));

      emit NewTransmission(
        r.hotVars.latestAggregatorRoundId,
        median,
        msg.sender,
        r.observations,
        r.observers,
        r.rawReportContext
      );
      // 用于与只支持旧事件的离线消费者的向后兼容性
      // 只支持旧事件
      emit NewRound(
        r.hotVars.latestAggregatorRoundId,
        address(0x0), // 使用零地址，因为我们没有任何人在这里“开始”这一轮
        block.timestamp
      );
      emit AnswerUpdated(
        median,
        r.hotVars.latestAggregatorRoundId,
        block.timestamp
      );

      validateAnswer(r.hotVars.latestAggregatorRoundId, median);
    }
    s_hotVars = r.hotVars;
    assert(initialGas < maxUint32);
    reimburseAndRewardOracles(uint32(initialGas), r.observers);
  }

  /*
   * v2 Aggregator接口
   */

  /**
   * @notice 最新报告的中位数
   */
  function latestAnswer()
    public
    override
    view
    virtual
    returns (int256)
  {
    return s_transmissions[s_hotVars.latestAggregatorRoundId].answer;
  }

  /**
   * @notice 上次报告被传输的块的时间戳
   */
  function latestTimestamp()
    public
    override
    view
    virtual
    returns (uint256)
  {
    return s_transmissions[s_hotVars.latestAggregatorRoundId].timestamp;
  }

  /**
   * @notice 最新报告的聚合器轮次（而不是OCR轮次）
   */
  function latestRound()
    public
    override
    view
    virtual
    returns (uint256)
  {
    return s_hotVars.latestAggregatorRoundId;
  }

  /**
   * @notice 报告从给定聚合器轮次（而不是OCR轮次）获取的中位数
   * @param _roundId 目标报告的聚合器轮次
   */
  function getAnswer(uint256 _roundId)
    public
    override
    view
    virtual
    returns (int256)
  {
    if (_roundId > 0xFFFFFFFF) { return 0; }
    return s_transmissions[uint32(_roundId)].answer;
  }

  /**
   * @notice 报告从给定聚合器轮次获取的块的时间戳
   * @param _roundId 目标报告的聚合器轮次
   */
  function getTimestamp(uint256 _roundId)
    public
    override
    view
    virtual
    returns (uint256)
  {
    if (_roundId > 0xFFFFFFFF) { return 0; }
    return s_transmissions[uint32(_roundId)].timestamp;
  }

  /*
   * v3 Aggregator接口
   */

  string constant private V3_NO_DATA_ERROR = "No data present";

  /**
   * @return 固定点格式存储答案，精度为多少位
   */
  uint8 immutable public override decimals;

  /**
   * @notice 聚合器合约版本
   */
  uint256 constant public override version = 4;

  string internal s_description;

  /**
   * @notice 可观察事物的人类可读描述
   */
  function description()
    public
    override
    view
    virtual
    returns (string memory)
  {
    return s_description;
  }

  /**
   * @notice 给定聚合器轮次的聚合器详细信息
   * @param _roundId 目标聚合器轮次（而不是OCR轮次）。必须适应uint32
   * @return roundId _roundId
   * @return answer 来自给定_roundId的报告的中位数
   * @return startedAt 包含给定_roundId的报告的块的时间戳
   * @return updatedAt 包含给定_roundId的报告的块的时间戳
   * @return answeredInRound _roundId
   */
  function getRoundData(uint80 _roundId)
    public
    override
    view
    virtual
    returns (
      uint80 roundId,
      int256 answer,
      uint256 startedAt,
      uint256 updatedAt,
      uint80 answeredInRound
    )
  {
    require(_roundId <= 0xFFFFFFFF, V3_NO_DATA_ERROR);
    Transmission memory transmission = s_transmissions[uint32(_roundId)];
    return (
      _roundId,
      transmission.answer,
      transmission.timestamp,
      transmission.timestamp,
      _roundId
    );
  }

  /**
   * @notice 最新传输的报告的聚合器详细信息
   * @return roundId 最新报告的聚合器轮次（而不是OCR轮次）
   * @return answer 最新报告的中位数
   * @return startedAt 包含最新报告的块的时间戳
   * @return updatedAt 包含最新报告的块的时间戳
   * @return answeredInRound 最新报告的聚合器轮次
   */
  function latestRoundData()
    public
    override
    view
    virtual
    returns (
      uint80 roundId,
      int256 answer,
      uint256 startedAt,
      uint256 updatedAt,
      uint80 answeredInRound
    )
  {
    roundId = s_hotVars.latestAggregatorRoundId;

    // 与现有FluxAggregator跳过以防止回滚的compatability
    // 需要这一行
    // require(roundId != 0, V3_NO_DATA_ERROR);

    Transmission memory transmission = s_transmissions[uint32(roundId)];
    return (
      roundId,
      transmission.answer,
      transmission.timestamp,
      transmission.timestamp,
      roundId
    );
  }
}